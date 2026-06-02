package rrc

import (
	"context"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Engine implements the RRC algorithm. Goroutine-safe at the level
// of one assemble per engine: Assemble holds an internal write lock
// for the duration of OnMessage+Select+MMR, and Fork / Merge serialize
// against it. Settings changes go through atomic engine replacement
// (see service/substrate.Holder.UpdateEngineConfig) — there is no
// in-place mutation surface and no externally-held lock.
//
// Scoring substrate: chunks (paragraph-sized slices of a message),
// resolved via a ChunkOracle. Each chunk carries an embedding vector
// (cached) used for cosine prefiltering; the reranker (bge-reranker-v2-m3)
// produces the primary relevance score. Max-over-chunk-pairs becomes
// the message-pair edge score — the strongest matching chunk
// determines the edge, so a long reference document like a character
// bible is represented by its most-relevant section for each query
// rather than by a diluted whole-document average.
//
// The score cache is global, persistent, keyed at chunk granularity
// so re-chunking or chunk-reordering is a no-op at the cache boundary.
// Messages are immutable → cached scores never become stale.
type Engine struct {
	mu         sync.Mutex
	scorer Scorer
	dag        *dag
	scores     *scoreCache
	cfg        EngineConfig
	oracle     ChunkOracle
}

// ChunkRef is a chunk's content plus optional cached vector and
// retrieval metadata.
type ChunkRef struct {
	MessageID  string
	ChunkIndex int
	Text       string
	// Vector is the cached embedding. May be nil if embedding hasn't
	// backfilled yet — the oracle's EnsureVector method fetches live.
	Vector []float32
	// RetrievalScore is the Layer-1 similarity score that surfaced this
	// candidate. Populated by ChunkOracle.NearestChunks; zero (and
	// meaningless) on refs returned by ChunksForMessages or EnsureVector.
	// Range [0, 1] for normalized similarity; the storage backend
	// converts its native distance metric to similarity at the seam.
	// The engine uses this when no scorer is configured — the
	// retrieval score then feeds edge formation directly instead of
	// being a precursor to the rerank pass.
	RetrievalScore float64
}

// ChunkOracle resolves chunks for messages and surfaces near-neighbors
// from the corpus by vector similarity. Implemented by the service
// layer on top of storage. The engine never talks to SQLite or HTTP
// directly; all I/O goes through this interface.
type ChunkOracle interface {
	// ChunksForMessages returns all chunks for the given message IDs,
	// each list ordered by chunk_index. Missing messages (no chunks
	// persisted) are simply absent from the returned map. Vectors are
	// populated from cache where available; nil vectors mean the
	// engine should call EnsureVector if it needs them.
	ChunksForMessages(ctx context.Context, messageIDs []string) (map[string][]ChunkRef, error)

	// EnsureVector returns a chunk's vector, embedding live and
	// persisting through to the cache if not yet stored. Called for
	// the brand-new message at the start of OnMessage (backfill may
	// not have caught it yet).
	EnsureVector(ctx context.Context, ref ChunkRef) ([]float32, error)

	// NearestChunks returns the top-k chunks by cosine similarity to
	// queryText, restricted to chunks satisfying predicate. The
	// implementation encodes queryText as a query (asymmetric models
	// apply a query-specific prompt; symmetric models encode plainly),
	// then runs an ANN index search (sqlite-vec etc.) — cost is
	// sub-linear in corpus size, the load-bearing call that honors
	// RRC's per-step invariance promise. Predicate must be compiled to
	// the backend's native filter form and pushed into the KNN search
	// (not post-filter, which collapses recall when the predicate is
	// selective).
	NearestChunks(ctx context.Context, queryText string, k int, predicate Predicate) ([]ChunkRef, error)

	// DiversityRerank applies Maximal Marginal Relevance (Carbonell &
	// Goldstein 1998) to the candidate set: effective(C) =
	// λ·orig(C) − (1−λ)·max_sim(C, kept). The oracle knows its
	// embedding shape — single-vector implementations do cosine over
	// mean-pooled chunk vectors; multi-vector implementations use
	// sum-of-max or whatever fits their model. The engine doesn't
	// commit to a vector representation.
	//
	// Returns candidates with EffectiveScore rewritten to the
	// MMR-adjusted value, ordered by adjusted score descending.
	// originalScores carries the pre-MMR scores keyed by message id
	// — the oracle needs them as the λ-weighted base of each
	// candidate's adjusted score.
	DiversityRerank(ctx context.Context, candidates []*pb.SelectedMessage, originalScores map[string]float64, lambda float64) ([]*pb.SelectedMessage, error)
}

// Option configures an Engine at construction. Pass options to
// NewEngine; the engine is fully formed when NewEngine returns and
// has no public mutation surface.
type Option func(*Engine)

// WithChunkOracle wires the chunk + vector resolver. Required for
// OnMessage to score; absent oracle ⇒ OnMessage returns
// ErrScorerUnavailable wrapping "no chunk oracle".
func WithChunkOracle(o ChunkOracle) Option {
	return func(e *Engine) { e.oracle = o }
}

// WithScorePersister installs the write-through hook fired on every
// new chunk-pair score. The service wires it to storage; nil disables
// persistence (test path).
func WithScorePersister(p ScorePersister) Option {
	return func(e *Engine) {
		if e.scores != nil {
			e.scores.setPersister(p)
		}
	}
}

// WithLoadedEdges hydrates the DAG with edges previously persisted.
// The kernel uses this on Bootstrap and on every engine swap so the
// new engine's in-memory DAG matches what's on disk.
func WithLoadedEdges(edges []*pb.Edge) Option {
	return func(e *Engine) {
		for _, edge := range edges {
			e.dag.AddEdge(edge)
		}
	}
}

// WithLoadedScores hydrates the chunk-pair score cache. Silent path:
// no write-back to the persister.
func WithLoadedScores(scores []PersistedScore) Option {
	return func(e *Engine) {
		for _, ps := range scores {
			e.scores.loadSilent(scoreKey{
				FromMsgID:    ps.FromMsgID,
				FromChunkIdx: ps.FromChunkIdx,
				ToMsgID:      ps.ToMsgID,
				ToChunkIdx:   ps.ToChunkIdx,
			}, ps.Score)
		}
	}
}

// NewEngine constructs an RRC engine. The scorer is the only
// required external dependency; everything else (oracle, persister,
// hydrated DAG / scores) flows in via Option. The engine is
// immutable post-construction — any setting change rebuilds.
func NewEngine(cfg EngineConfig, scorer Scorer, opts ...Option) *Engine {
	e := &Engine{
		scorer: scorer,
		dag:    newDAG(),
		scores: newScoreCache(),
		cfg:    cfg,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Config returns a copy of the engine configuration.
func (e *Engine) Config() EngineConfig { return e.cfg }

// OnMessage scores a new message against its predecessors in the
// corpus and creates edges above EdgeThreshold. Hot path:
//
//  1. Collect prior message IDs (exclude self, exclude empty-text).
//  2. Resolve chunks (+vectors) for priors and the new msg via oracle.
//  3. For each new-chunk:
//     a. For each prior chunk: look up cached score in ScoreCache.
//     b. Unscored chunk-pairs → cosine-prefilter on embeddings, take
//        top-K candidates (per new-chunk) for reranker scoring.
//     c. Single rerank call per new-chunk: query = new chunk text,
//        candidates = top-K prior-chunk texts, get aligned scores.
//     d. Cache + persist each chunk-pair score.
//  4. Aggregate chunk-pair scores to a single message-pair score via
//     max. Emit edges where max-score ≥ EdgeThreshold.
//
// If the scorer is unavailable, returns a wrapped error — the
// service MUST log this; a silent no-op leaves RRC dark.
//
// Caller must not call OnMessage concurrently with itself, Select,
// ApplyMMR, Fork, or Merge on the same engine. Assemble holds the
// engine mutex around its OnMessage→Select→MMR sequence.
//
// Returns the new edges, an OnMessageTelemetry record (always valid;
// callers may discard), and any error.
func (e *Engine) OnMessage(ctx context.Context, msg *pb.Message, corpus []*pb.Message) ([]*pb.Edge, OnMessageTelemetry, error) {
	if len(corpus) == 0 {
		return nil, OnMessageTelemetry{}, nil
	}
	if e.oracle == nil {
		// Without a chunk oracle we cannot score. Fail fast rather
		// than pretending to work — the service wires this at startup.
		return nil, OnMessageTelemetry{}, fmt.Errorf("%w: no chunk oracle configured", ErrScorerUnavailable)
	}
	// A nil scorer is allowed: the oracle's Layer-1 retrieval score
	// (ChunkRef.RetrievalScore) feeds edge formation directly. The
	// engine doesn't take a position on whether RRC is one-layer or
	// two-layer; that's the substrate's choice. The adaptive gates
	// (MinBatchStdDev, ZScoreThreshold) describe properties of reranker
	// score distributions and only fire when a reranker is configured.

	t0 := time.Now()

	// Filter priors: drop self and empty-text messages.
	priors := make([]*pb.Message, 0, len(corpus))
	var priorsSkipped int
	for _, m := range corpus {
		if m.Id == msg.Id {
			continue
		}
		if textFromMessage(m) == "" {
			priorsSkipped++
			continue
		}
		priors = append(priors, m)
	}
	if len(priors) == 0 {
		log.Printf("RRC: OnMessage target=%s thread=%s priors=0 (corpus=%d skipped=%d)",
			msg.Id, msg.ThreadId, len(corpus), priorsSkipped)
		return nil, OnMessageTelemetry{DurationMs: time.Since(t0).Milliseconds()}, nil
	}

	// Resolve chunks for the new message only — prior chunks come back
	// from the oracle's NearestChunks call, so we don't need to fetch
	// them eagerly for the whole corpus.
	chunkMap, err := e.oracle.ChunksForMessages(ctx, []string{msg.Id})
	if err != nil {
		return nil, OnMessageTelemetry{}, fmt.Errorf("resolve chunks: %w", err)
	}
	newChunks := chunkMap[msg.Id]
	if len(newChunks) == 0 {
		// No chunks for the new message — either chunker wasn't wired
		// when it was stored (edge case) or its text came out empty.
		// Nothing to score against; surface diagnostically.
		log.Printf("RRC: OnMessage target=%s thread=%s chunks=0 — no scoring possible", msg.Id, msg.ThreadId)
		return nil, OnMessageTelemetry{DurationMs: time.Since(t0).Milliseconds()}, nil
	}

	// Ensure vectors for new chunks (oracle embeds + persists on miss).
	// Storing now means the new chunks become retrievable as priors
	// for *later* turns. They could in principle surface in this turn's
	// NearestChunks return; we filter those out at the engine layer
	// (a message can't be its own prerequisite).
	for i := range newChunks {
		if newChunks[i].Vector == nil {
			v, verr := e.oracle.EnsureVector(ctx, newChunks[i])
			if verr == nil {
				newChunks[i].Vector = v
			}
		}
	}

	// Build a fast prior-message lookup so we can map ChunkRefs back to
	// pb.Message metadata (thread_id etc.) for edge formation. Self-msg
	// is intentionally excluded.
	priorByID := make(map[string]*pb.Message, len(priors))
	for _, p := range priors {
		priorByID[p.Id] = p
	}

	// Iterate new chunks; each is its own retrieval+rerank "query".
	// bestScore tracks the max reranker score per prior *message* —
	// the aggregation from chunk-pair to message-pair per protocol §6.5
	// implementation freedom (we chose max-merge for its fact-level
	// retrieval semantics).
	bestScore := make(map[string]float64, len(priors))
	var totalCached, totalReranked, totalRetrieved int

	// scopePred is the predicate handed to NearestChunks. The corpus
	// passed to OnMessage is already scope-filtered upstream, so a
	// no-op PredAll here returns candidates from the same world the
	// engine sees. A future refactor that lifts scope into the engine
	// would build a richer predicate here (PredScope, PredAnd of
	// PredScope + PredHasAttachments, etc.) without changing the
	// retrieval call shape.
	scopePred := Predicate(PredAll{})

	for _, nc := range newChunks {
		// Sub-linear retrieval: oracle returns up to RerankTopK chunks
		// nearest to nc.Text by query-side cosine, restricted to the
		// scope predicate. This is the load-bearing call that honors
		// per-step compute invariance — cost grows with K, not with
		// corpus size.
		k := e.cfg.RerankTopK
		if k <= 0 {
			k = 64
		}
		retrieved, rerr := e.oracle.NearestChunks(ctx, nc.Text, k, scopePred)
		if rerr != nil {
			return nil, OnMessageTelemetry{}, fmt.Errorf("nearest chunks: %w", rerr)
		}

		// Filter out self-message chunks (a message can't be its own
		// prerequisite) and chunks whose source message isn't in the
		// scope-filtered corpus the engine was given.
		filtered := make([]ChunkRef, 0, len(retrieved))
		for _, c := range retrieved {
			if c.MessageID == msg.Id {
				continue
			}
			if _, ok := priorByID[c.MessageID]; !ok {
				continue
			}
			filtered = append(filtered, c)
		}
		totalRetrieved += len(filtered)

		// Cache check: split into cached (use stored score) and
		// uncached (will go to the reranker).
		type scoredCand struct {
			ref      ChunkRef
			cached   bool
			score    float64
		}
		cands := make([]scoredCand, len(filtered))
		uncachedIdx := make([]int, 0, len(filtered))
		for i, c := range filtered {
			if s, ok := e.scores.get(c.MessageID, c.ChunkIndex, msg.Id, nc.ChunkIndex); ok {
				cands[i] = scoredCand{ref: c, cached: true, score: s}
			} else {
				cands[i] = scoredCand{ref: c}
				uncachedIdx = append(uncachedIdx, i)
			}
		}
		totalCached += len(filtered) - len(uncachedIdx)

		// Score the uncached candidates. With a scorer configured,
		// one rerank call per new chunk produces refined scores; without
		// one, the retrieval score from Layer 1 (ChunkRef.RetrievalScore)
		// is the candidate's score directly.
		if len(uncachedIdx) > 0 {
			if e.scorer != nil {
				candTexts := make([]string, len(uncachedIdx))
				for i, idx := range uncachedIdx {
					candTexts[i] = cands[idx].ref.Text
				}
				scores, rerr := e.scorer.Score(ctx, nc.Text, candTexts)
				if rerr != nil {
					return nil, OnMessageTelemetry{}, fmt.Errorf("%w: %v", ErrScorerFailed, rerr)
				}
				var crossCount, sameCount int
				var crossMax, sameMax float64
				for i, idx := range uncachedIdx {
					s := 0.0
					if i < len(scores) {
						s = scores[i]
					}
					cands[idx].score = s
					e.scores.set(cands[idx].ref.MessageID, cands[idx].ref.ChunkIndex, msg.Id, nc.ChunkIndex, s)
					if priorByID[cands[idx].ref.MessageID].ThreadId == msg.ThreadId {
						sameCount++
						if s > sameMax {
							sameMax = s
						}
					} else {
						crossCount++
						if s > crossMax {
							crossMax = s
						}
					}
				}
				log.Printf("RRC: rerank thread=%s new_chunk=%d top-%d: same-thread=%d(max=%.3f) cross-thread=%d(max=%.3f)",
					msg.ThreadId, nc.ChunkIndex, len(uncachedIdx), sameCount, sameMax, crossCount, crossMax)
				totalReranked += len(uncachedIdx)
			} else {
				// Layer-1-only path: oracle's RetrievalScore is the
				// candidate's score. Cache writes still happen so the
				// persister can record the run; the model_id tag
				// distinguishes Layer-1 entries when both modes coexist
				// in the same store across configuration changes.
				for _, idx := range uncachedIdx {
					s := cands[idx].ref.RetrievalScore
					cands[idx].score = s
					e.scores.set(cands[idx].ref.MessageID, cands[idx].ref.ChunkIndex, msg.Id, nc.ChunkIndex, s)
				}
			}
		}

		// Aggregate chunk-pair scores into per-prior-message max.
		for _, sc := range cands {
			priorMsgID := sc.ref.MessageID
			if sc.score > bestScore[priorMsgID] {
				bestScore[priorMsgID] = sc.score
			}
		}
	}

	// Emit message-pair edges.
	//
	// Edge formation gates on raw CE. Earlier designs combined CE with temporal proximity
	// — that introduced an asymmetric score distribution between
	// same-thread (CE+temporal) and cross-thread (CE-only) candidates
	// and forced two thresholds. Temporal-as-edge-signal duplicated
	// Radius's job (Radius unconditionally pads same-thread tail on
	// the wire); outside that window temporal contribution decayed
	// to negligible. Edge formation is now a single signal scored
	// against a single threshold across both regimes.
	//
	// Build the per-query candidate batch for the adaptive gates.
	// Only priors that actually had chunks rescored by the reranker
	// form the distribution — the rest have bestScore[id]=0 and
	// would depress mean / inflate stddev artificially, making the
	// gates fire against the wrong baseline.
	type candidate struct {
		prior *pb.Message
		ce    float64
	}
	candidates := make([]candidate, 0, len(priors))
	for _, p := range priors {
		if _, rescored := bestScore[p.Id]; !rescored {
			continue
		}
		candidates = append(candidates, candidate{prior: p, ce: bestScore[p.Id]})
	}

	// Compute batch mean and stddev for the adaptive gates. Single
	// pass for mean, second pass for variance — Welford would be
	// unnecessary at N~64.
	var batchMean, batchStddev float64
	if len(candidates) > 0 {
		for _, c := range candidates {
			batchMean += c.ce
		}
		batchMean /= float64(len(candidates))
		var variance float64
		for _, c := range candidates {
			d := c.ce - batchMean
			variance += d * d
		}
		variance /= float64(len(candidates))
		batchStddev = math.Sqrt(variance)
	}

	// Gate 3 (meta-discriminator): a flat distribution means the
	// reranker couldn't discriminate on this query. Per protocol
	// §6.4, Selection SHOULD return nothing rather than
	// low-confidence results. Zero MinBatchStdDev disables.
	// Reranker-only gate — Layer-1 cosine distributions cluster
	// tighter; the stddev threshold here is calibrated for reranker
	// score shapes and doesn't transfer.
	if e.scorer != nil && e.cfg.MinBatchStdDev > 0 && batchStddev < e.cfg.MinBatchStdDev && len(candidates) > 0 {
		log.Printf("RRC: OnMessage batch indiscriminate target=%s thread=%s candidates=%d mean=%.3f stddev=%.3f (< MinBatchStdDev=%.3f) — no edges this round",
			msg.Id, msg.ThreadId, len(candidates), batchMean, batchStddev, e.cfg.MinBatchStdDev)
		return nil, OnMessageTelemetry{
			DurationMs:       time.Since(t0).Milliseconds(),
			PriorsConsidered: len(priors),
			CandidatesScored: len(candidates),
			Reranked:         totalReranked,
		}, nil
	}

	var edges []*pb.Edge
	var maxEdge, sumEdge float64
	var edgeCount int
	var skippedAbsolute, skippedZScore int
	for _, c := range candidates {
		// Gate 1: absolute threshold. Cuts noise floor (candidates
		// with CE too low to be plausible prereqs at all).
		if c.ce < e.cfg.EdgeThreshold {
			skippedAbsolute++
			continue
		}
		// Gate 2: adaptive z-score. Cuts high-floor-but-undifferentiated
		// candidates (cluster of similar scores where nothing truly
		// stands out). Zero ZScoreThreshold or zero stddev disables.
		// Reranker-only gate — same reason as Gate 3 above.
		if e.scorer != nil && e.cfg.ZScoreThreshold > 0 && batchStddev > 0 {
			z := (c.ce - batchMean) / batchStddev
			if z < e.cfg.ZScoreThreshold {
				skippedZScore++
				continue
			}
		}
		edgeCount++
		sumEdge += c.ce
		if c.ce > maxEdge {
			maxEdge = c.ce
		}
		edge := &pb.Edge{
			FromMessageId:     c.prior.Id,
			ToMessageId:       msg.Id,
			Score:             float32(c.ce),
			Source:            pb.EdgeSource_EDGE_SOURCE_CROSS_ENCODER,
			CrossEncoderScore: float32(c.ce),
			DetectedAt:        timestamppb.Now(),
			FromThreadId:      c.prior.ThreadId,
			ToThreadId:        msg.ThreadId,
		}
		e.dag.AddEdge(edge)
		edges = append(edges, edge)
	}

	avgEdge := 0.0
	if edgeCount > 0 {
		avgEdge = sumEdge / float64(edgeCount)
	}
	dur := time.Since(t0)
	log.Printf("RRC: OnMessage target=%s thread=%s corpus=%d priors=%d skipped=%d newChunks=%d retrieved=%d cached=%d reranked=%d candidates=%d mean=%.3f stddev=%.3f gated(abs=%d,z=%d) edgesNew=%d maxCE=%.3f avgCE=%.3f dur=%v",
		msg.Id, msg.ThreadId,
		len(corpus), len(priors), priorsSkipped,
		len(newChunks), totalRetrieved,
		totalCached, totalReranked,
		len(candidates), batchMean, batchStddev,
		skippedAbsolute, skippedZScore,
		len(edges), maxEdge, avgEdge, dur)
	return edges, OnMessageTelemetry{
		DurationMs:       dur.Milliseconds(),
		PriorsConsidered: len(priors),
		CandidatesScored: len(candidates),
		Reranked:         totalReranked,
		EdgesFormed:      len(edges),
	}, nil
}

// OnMessageTelemetry is the per-call timing + counter record from
// Engine.OnMessage. Returned alongside edges so callers that want to
// aggregate into a higher-level trace (per-tick telemetry, structured
// logs) can do so without re-instrumenting the engine. Always valid:
// even error paths return a zero-or-partial value so callers can
// unconditionally read the fields they care about.
type OnMessageTelemetry struct {
	DurationMs       int64 // wall clock from OnMessage entry to return
	PriorsConsidered int   // prior messages after self/empty filter
	CandidatesScored int   // priors that had at least one chunk-pair scored
	Reranked         int   // chunk-pair scores produced by the scorer (excludes cache hits)
	EdgesFormed      int   // edges emitted after gates
}

// inSlice is a tiny helper for the aggregation loop — go doesn't have
// slices.Contains in our 1.24 toolchain ambient set.
func inSlice(xs []int, target int) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}

// Select performs prerequisite selection for a prompt. Best-first backward
// traversal through the DAG, score floor cutoff, transitive reduction.
func (e *Engine) Select(promptID string, scope pb.SelectionScope, threadID string) (*pb.SelectionResult, error) {
	if scope == pb.SelectionScope_SELECTION_SCOPE_THREAD && threadID == "" {
		return nil, ErrThreadNotFound
	}

	selected, belowFloor := extractSubgraph(e.dag, promptID, threadID, scope, e.cfg)
	selected = transitiveReduction(selected)

	result := &pb.SelectionResult{
		EventId:  fmt.Sprintf("sel-%s", promptID),
		Scope:    scope,
		ThreadId: threadID,
	}

	selectedIDs := make(map[string]bool)
	for _, s := range selected {
		selectedIDs[s.MessageID] = true
		result.Selected = append(result.Selected, &pb.SelectedMessage{
			MessageId:      s.MessageID,
			EffectiveScore: float32(s.EffectiveScore),
			HopDepth:       int32(s.HopDepth),
			ViaEdges:       s.ViaEdges,
			ThreadId:       s.ThreadID,
			CrossThread:    s.CrossThread,
		})
	}

	for msgID, score := range belowFloor {
		if !selectedIDs[msgID] {
			result.Excluded = append(result.Excluded, &pb.ExcludedMessage{
				MessageId: msgID,
				Reason:    pb.ExclusionReason_EXCLUSION_REASON_BELOW_THRESHOLD,
				Score:     float32(score),
			})
		}
	}

	for _, edge := range e.dag.Prerequisites(promptID) {
		fromID := edge.FromMessageId
		if selectedIDs[fromID] || belowFloor[fromID] > 0 {
			continue
		}
		if !scopeAllows(edge, threadID, scope) {
			result.Excluded = append(result.Excluded, &pb.ExcludedMessage{
				MessageId: fromID,
				Reason:    pb.ExclusionReason_EXCLUSION_REASON_UNSPECIFIED,
				Score:     edge.Score,
			})
		}
	}

	return result, nil
}

// ApplyMMR reranks a Selected slice for diversity via the oracle.
// The engine sequences the algorithm but doesn't compute the
// similarity itself — that knowledge belongs to the oracle, which
// knows whether its embedding is single-vector (cosine over mean-pool)
// or multi-vector (sum-of-max) or something else.
//
// Purpose per the design plan: collapse near-duplicate entries in the
// Selected pool — e.g. nine variations of a model's process-thinking
// (*"let me check what chapter we're on"*) that all score similarly
// under similarity-only rerankers. After MMR, the first keeps its
// score, the other eight take a penalty proportional to their
// redundancy with the kept set. Downstream the rrcllm shed loop uses
// the updated effective score, so under budget pressure the redundant
// copies fall first and leave room for the distinct content priors
// they were crowding out.
//
// No hard K cap. Hyperselection is empirical; this function reorders
// and rescores but doesn't drop anything — the budget shed does the
// actual shrinking, and does it principled-ly based on the MMR-
// adjusted scores.
//
// Requires the ChunkOracle to be wired; without it, returns selected
// unchanged with an error. λ outside [0,1] or zero-length selected is
// a no-op.
func (e *Engine) ApplyMMR(ctx context.Context, selected []*pb.SelectedMessage, lambda float64) ([]*pb.SelectedMessage, error) {
	if len(selected) <= 1 || lambda <= 0 || lambda >= 1 {
		// lambda=1 is pure-relevance (today's behavior); lambda=0 is
		// pure-diversity (wrong for a relevance task). Either extreme
		// is a pass-through.
		return selected, nil
	}
	if e.oracle == nil {
		return selected, fmt.Errorf("ApplyMMR: no chunk oracle configured")
	}
	orig := make(map[string]float64, len(selected))
	for _, s := range selected {
		orig[s.MessageId] = float64(s.EffectiveScore)
	}
	return e.oracle.DiversityRerank(ctx, selected, orig, lambda)
}

// Fork creates an ephemeral engine for a subagent thread. Inherits a
// snapshot of the parent's DAG and score cache; same scorer,
// oracle, and config. The parent's persister is reused so any new
// edges the fork emits write through to the same storage.
//
// Internal lock around the snapshot read so a concurrent OnMessage
// in the parent doesn't yield a partial copy.
func (e *Engine) Fork() *Engine {
	e.mu.Lock()
	defer e.mu.Unlock()

	fork := &Engine{
		scorer: e.scorer,
		dag:    newDAG(),
		scores: newScoreCache(),
		cfg:    e.cfg,
		oracle: e.oracle,
	}
	fork.scores.setPersister(e.scores.persist)
	for _, edge := range e.dag.AllEdges() {
		fork.dag.AddEdge(edge)
	}
	for k, v := range e.scores.all() {
		fork.scores.loadSilent(k, v)
	}
	return fork
}

// Merge integrates a fork's edges and any new chunk-pair scores into
// the parent. Idempotent: edges and scores already present in the
// parent are not duplicated. Scores new to the parent flow through
// the parent's persister.
func (e *Engine) Merge(fork *Engine) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, edge := range fork.dag.AllEdges() {
		e.dag.AddEdge(edge)
	}
	for k, v := range fork.scores.all() {
		if _, exists := e.scores.get(k.FromMsgID, k.FromChunkIdx, k.ToMsgID, k.ToChunkIdx); !exists {
			e.scores.set(k.FromMsgID, k.FromChunkIdx, k.ToMsgID, k.ToChunkIdx, v)
		}
	}
	return nil
}

// --- internal helpers ---

// textFromMessage delegates to TextFromBlocks. Kept as a tiny shim so
// existing internal call sites (the empty-text filter in OnMessage)
// don't all need updating; it has no other purpose.
func textFromMessage(msg *pb.Message) string { return TextFromBlocks(msg.Content) }

