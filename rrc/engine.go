package rrc

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"sync"
	"time"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Engine implements the RRC algorithm. Goroutine-safe at the level
// of one operation per call: callers that need to serialize multi-
// call sequences (e.g., OnMessage followed atomically by Select)
// take Engine.Lock / Engine.Unlock around the whole sequence.
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
	mu         sync.RWMutex
	classifier Classifier
	entailer   Entailer    // optional — composite NLI stage layered on top of classifier
	dag        *DAG
	scores     *scoreCache
	cfg        EngineConfig
	oracle     ChunkOracle // optional — nil falls back to full-text-as-single-chunk fallback
}

// ChunkRef is a chunk's content plus optional cached vector.
type ChunkRef struct {
	MessageID  string
	ChunkIndex int
	Text       string
	// Vector is the cached embedding. May be nil if embedding hasn't
	// backfilled yet — the oracle's EnsureVector method fetches live.
	Vector []float32
}

// ChunkOracle resolves chunks for messages. Implemented by the
// service layer on top of storage. The engine never talks to SQLite
// or HTTP directly; all I/O goes through this interface.
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
}

// NewEngine creates an RRC engine. The classifier is the only
// required external dependency. Use SetEntailer / SetChunkOracle /
// SetScorePersister to wire the optional substrate before
// running OnMessage.
func NewEngine(cfg EngineConfig, classifier Classifier) *Engine {
	return &Engine{
		classifier: classifier,
		dag:        newDAG(),
		scores:     newScoreCache(),
		cfg:        cfg,
	}
}

// Lock / Unlock / RLock / RUnlock expose the engine's internal
// RWMutex. Callers serialize multi-call sequences by holding Lock
// around the whole sequence. Single-call entry points (OnMessage,
// Select, ApplyMMR, Snapshot) acquire the lock internally — the
// public Lock methods exist for the agent runner's atomic
// "OnMessage(query); Select(query.Id)" sequence inside one round.
func (e *Engine) Lock()    { e.mu.Lock() }
func (e *Engine) Unlock()  { e.mu.Unlock() }
func (e *Engine) RLock()   { e.mu.RLock() }
func (e *Engine) RUnlock() { e.mu.RUnlock() }

// SetClassifier replaces the classifier. Caller must hold Lock().
func (e *Engine) SetClassifier(c Classifier) { e.classifier = c }

// SetEntailer wires the optional NLI stage. Passing nil disables it.
// When set, OnMessage fuses NLI entailment probability into each
// rerank score via NLIFusionWeight α:
//
//	fused_raw = α · bge_rerank + (1-α) · nli_entail
//
// Caller must hold Lock().
func (e *Engine) SetEntailer(en Entailer) { e.entailer = en }

// SetChunkOracle wires the chunk+vector provider. Caller must hold Lock().
func (e *Engine) SetChunkOracle(o ChunkOracle) { e.oracle = o }

// SetScorePersister installs the write-through hook fired on every
// new chunk-pair score. The service wires it to storage at boot;
// passing nil disables persistence (test path). Caller must hold Lock().
func (e *Engine) SetScorePersister(p ScorePersister) {
	if e.scores != nil {
		e.scores.setPersister(p)
	}
}

// RadiusSize is the protocol §2 Radius window — the count of most-recent
// thread messages the Network Regime (§3.3) inserts between Selected
// and Current Turn.
func (e *Engine) RadiusSize() int { return e.cfg.RadiusSize }

// Config returns a copy of the current engine configuration.
func (e *Engine) Config() EngineConfig { return e.cfg }

// UpdateConfig swaps the engine's configuration atomically. The DAG
// is unchanged — the stored edges retain their raw score components
// (reranker CE, temporal proximity) and are re-projected under the
// new config at walk time via edgeScoreUnderConfig. This means
// config changes take effect on the next Select call, retroactively,
// with no rebuild step. Threshold tightening immediately hides
// edges that no longer qualify; threshold loosening restores them;
// weight changes recompute fused scores. All without re-invoking
// the reranker.
//
// Future OnMessage calls will use the new config for scoring new
// edges. The score cache is config-independent (stores raw reranker
// outputs), so cached chunk-pair scores remain valid.
func (e *Engine) UpdateConfig(cfg EngineConfig) { e.cfg = cfg }

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
// If the classifier is unavailable, returns a wrapped error — the
// service MUST log this; a silent no-op leaves RRC dark.
func (e *Engine) OnMessage(ctx context.Context, msg *pb.Message, corpus []*pb.Message) ([]*pb.Edge, error) {
	if len(corpus) == 0 {
		return nil, nil
	}
	if e.classifier == nil {
		return nil, ErrClassifierUnavailable
	}
	if e.oracle == nil {
		// Without a chunk oracle we cannot score. Fail fast rather
		// than pretending to work — the service wires this at startup.
		return nil, fmt.Errorf("%w: no chunk oracle configured", ErrClassifierUnavailable)
	}

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
		return nil, nil
	}

	// Resolve chunks for everyone in one query.
	wantIDs := make([]string, 0, len(priors)+1)
	wantIDs = append(wantIDs, msg.Id)
	for _, p := range priors {
		wantIDs = append(wantIDs, p.Id)
	}
	chunkMap, err := e.oracle.ChunksForMessages(ctx, wantIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve chunks: %w", err)
	}
	newChunks := chunkMap[msg.Id]
	if len(newChunks) == 0 {
		// No chunks for the new message — either chunker wasn't wired
		// when it was stored (edge case) or its text came out empty.
		// Nothing to score against; surface diagnostically.
		log.Printf("RRC: OnMessage target=%s thread=%s chunks=0 — no scoring possible", msg.Id, msg.ThreadId)
		return nil, nil
	}

	// Ensure vectors for new chunks (oracle embeds live on miss).
	for i := range newChunks {
		if newChunks[i].Vector == nil {
			v, verr := e.oracle.EnsureVector(ctx, newChunks[i])
			if verr == nil {
				newChunks[i].Vector = v
			}
		}
	}

	// Build a flat prior-chunk index. Each entry points back to its
	// source message and its chunk-ref with vector (if any). We keep
	// one flat list per new-chunk so rerank-set membership is per-pair.
	type priorChunk struct {
		msgIdx  int // index into priors[]
		chunk   ChunkRef
	}
	var priorChunks []priorChunk
	for pi, p := range priors {
		for _, c := range chunkMap[p.Id] {
			priorChunks = append(priorChunks, priorChunk{msgIdx: pi, chunk: c})
		}
	}
	if len(priorChunks) == 0 {
		log.Printf("RRC: OnMessage target=%s thread=%s priorChunks=0 (priors=%d had no chunks)",
			msg.Id, msg.ThreadId, len(priors))
		return nil, nil
	}

	// Iterate new chunks; each is its own rerank "query".
	// bestScore tracks the max reranker score per prior *message* —
	// the aggregation from chunk-pair to message-pair per protocol §6.5
	// implementation freedom (we chose max-merge for its fact-level
	// retrieval semantics).
	bestScore := make(map[string]float64, len(priors))
	bestCE := make(map[string]float64, len(priors)) // same as bestScore; tracked separately in case weights evolve
	var totalCached, totalReranked int

	for _, nc := range newChunks {
		// Cache lookup per prior chunk.
		type pairState struct {
			cached    bool
			score     float64
			localIdx  int // into priorChunks
		}
		pairStates := make([]pairState, len(priorChunks))
		var unscoredIdx []int
		for j, pc := range priorChunks {
			if s, ok := e.scores.get(pc.chunk.MessageID, pc.chunk.ChunkIndex, msg.Id, nc.ChunkIndex); ok {
				pairStates[j] = pairState{cached: true, score: s, localIdx: j}
			} else {
				unscoredIdx = append(unscoredIdx, j)
			}
		}
		totalCached += len(priorChunks) - len(unscoredIdx)

		// Cosine prefilter on unscored — rank priors by cosine(new_chunk, prior_chunk).
		// Vectors may be nil for chunks not yet embedded; those go to the tail of the ranking.
		if nc.Vector != nil {
			simScore := make(map[int]float64, len(unscoredIdx))
			for _, j := range unscoredIdx {
				if v := priorChunks[j].chunk.Vector; v != nil {
					simScore[j] = cosine(nc.Vector, v)
				}
			}
			sort.SliceStable(unscoredIdx, func(a, b int) bool {
				return simScore[unscoredIdx[a]] > simScore[unscoredIdx[b]]
			})
		}
		// Cap at RerankTopK so we don't blow reranker latency on long threads.
		k := e.cfg.RerankTopK
		if k <= 0 || k > len(unscoredIdx) {
			k = len(unscoredIdx)
		}
		rerankSet := unscoredIdx[:k]

		// One rerank call per new chunk: query = new chunk text, candidates = selected prior chunks.
		if len(rerankSet) > 0 {
			candidates := make([]string, len(rerankSet))
			for i, j := range rerankSet {
				candidates[i] = priorChunks[j].chunk.Text
			}
			scores, rerr := e.classifier.Rerank(ctx, nc.Text, candidates)
			if rerr != nil {
				return nil, fmt.Errorf("%w: %v", ErrClassifierFailed, rerr)
			}
			// Composite NLI stage — fuse entailment on the same
			// top-K the reranker just scored. Guard: only when
			// entailer is wired AND NLIFusionWeight is in (0,1).
			// At the extremes (0 or 1), the fusion degenerates to
			// NLI-only or bge-only respectively, which callers
			// should express by un-wiring the entailer rather than
			// paying the NLI round-trip for a no-op fusion.
			//
			// Failure propagates as ErrClassifierFailed, same as a
			// reranker failure — when the user wires an entailer
			// they've chosen to make it part of the scoring
			// substrate, and silent fusion-skipped-for-this-round
			// would leave the caller unaware that their composite
			// pipeline is running one-legged.
			if e.entailer != nil && e.cfg.NLIFusionWeight > 0 && e.cfg.NLIFusionWeight < 1 && len(candidates) > 0 {
				nliScores, nerr := e.entailer.Entail(ctx, nc.Text, candidates)
				if nerr != nil {
					return nil, fmt.Errorf("%w: entailer: %v", ErrClassifierFailed, nerr)
				}
				if len(nliScores) != len(scores) {
					return nil, fmt.Errorf("%w: entailer returned %d scores for %d candidates",
						ErrClassifierFailed, len(nliScores), len(scores))
				}
				alpha := e.cfg.NLIFusionWeight
				for i := range scores {
					scores[i] = alpha*scores[i] + (1-alpha)*nliScores[i]
				}
			}
			// Cross-thread visibility: count how many of the reranked
			// candidates came from threads other than the current one,
			// and what their max score was. Makes "scope=all_threads
			// selected 0" diagnosable — is the cosine prefilter even
			// surfacing cross-thread chunks, and what does the reranker
			// think of them?
			var crossCount, sameCount int
			var crossMax, sameMax float64
			for i, j := range rerankSet {
				pc := priorChunks[j]
				s := 0.0
				if i < len(scores) {
					s = scores[i]
				}
				if priors[pc.msgIdx].ThreadId == msg.ThreadId {
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
				msg.ThreadId, nc.ChunkIndex, len(rerankSet), sameCount, sameMax, crossCount, crossMax)
			for i, j := range rerankSet {
				var s float64
				if i < len(scores) {
					s = scores[i]
				}
				pairStates[j] = pairState{cached: false, score: s, localIdx: j}
				// Writes through scores.persist → InsertChunkScore → SQLite.
				e.scores.set(priorChunks[j].chunk.MessageID, priorChunks[j].chunk.ChunkIndex, msg.Id, nc.ChunkIndex, s)
			}
			totalReranked += len(rerankSet)
		}

		// Aggregate: for each pair (scored or cached), update the
		// per-prior-message max.
		for j, ps := range pairStates {
			if !ps.cached && (len(rerankSet) == 0 || !inSlice(rerankSet, j)) {
				// Below the top-K cut and not cached — no score for this pair.
				continue
			}
			priorMsgID := priors[priorChunks[j].msgIdx].Id
			if ps.score > bestScore[priorMsgID] {
				bestScore[priorMsgID] = ps.score
				bestCE[priorMsgID] = ps.score
			}
		}
	}

	// Emit message-pair edges.
	//
	// Prerequisite detection has two orthogonal axes that both contribute
	// to whether a prior message is a prerequisite of the current one:
	//
	//   1. Semantic (reranker CE score). "This content is about the same
	//      thing the current turn is producing." Captured by the cross-
	//      encoder's chunk-pair scoring.
	//   2. Structural / temporal (trajectory proximity). "This is what
	//      the current turn is continuing from." Not captured by the
	//      reranker — a focused autonomous run where every prior is on-
	//      topic flattens reranker signal, and conversely a sharp topic
	//      pivot makes the immediately-prior turn look irrelevant
	//      semantically when it's structurally critical.
	//
	// FuseScore(WeightCE*reranker + WeightTemp*temporal) combines both
	// into a single score. The edge forms iff that fused score clears
	// EdgeThreshold. Same-thread and cross-thread are treated uniformly
	// here — the temporal term is near-zero for cross-thread pairs
	// (positions diverge across threads), so structural signal only
	// boosts same-thread priors meaningfully. Per protocol §6.4
	// (Discriminative), edges only form when the combined signal is
	// above the configured threshold.
	// Build the per-query candidate batch for the adaptive gates.
	// Only priors that actually had chunks rescored by the reranker
	// form the distribution — the rest have bestScore[id]=0 and
	// would depress mean / inflate stddev artificially, making the
	// gates fire against the wrong baseline.
	type candidate struct {
		prior    *pb.Message
		ce       float64
		temporal float64
		fused    float64
	}
	candidates := make([]candidate, 0, len(priors))
	for _, p := range priors {
		if _, rescored := bestCE[p.Id]; !rescored {
			continue
		}
		s := bestScore[p.Id]
		temporal := TemporalProximity(p.Position, msg.Position)
		fused := FuseScore(e.cfg, s, temporal)
		candidates = append(candidates, candidate{
			prior: p, ce: s, temporal: temporal, fused: fused,
		})
	}

	// Compute batch mean and stddev for the adaptive gates. Single
	// pass for mean, second pass for variance — Welford would be
	// unnecessary at N~64.
	var batchMean, batchStddev float64
	if len(candidates) > 0 {
		for _, c := range candidates {
			batchMean += c.fused
		}
		batchMean /= float64(len(candidates))
		var variance float64
		for _, c := range candidates {
			d := c.fused - batchMean
			variance += d * d
		}
		variance /= float64(len(candidates))
		batchStddev = math.Sqrt(variance)
	}

	// Gate 3 (meta-discriminator): a flat distribution means the
	// reranker couldn't discriminate on this query. Per protocol
	// §6.4, Selection SHOULD return nothing rather than
	// low-confidence results. Zero MinBatchStdDev disables.
	if e.cfg.MinBatchStdDev > 0 && batchStddev < e.cfg.MinBatchStdDev && len(candidates) > 0 {
		log.Printf("RRC: OnMessage batch indiscriminate target=%s thread=%s candidates=%d mean=%.3f stddev=%.3f (< MinBatchStdDev=%.3f) — no edges this round",
			msg.Id, msg.ThreadId, len(candidates), batchMean, batchStddev, e.cfg.MinBatchStdDev)
		return nil, nil
	}

	var edges []*pb.Edge
	var maxEdge, sumEdge float64
	var edgeCount int
	var skippedAbsolute, skippedZScore int
	for _, c := range candidates {
		// Gate 1: absolute threshold. Cuts noise floor (candidates
		// with fused score too low to be plausible prereqs at all).
		if c.fused < e.cfg.EdgeThreshold {
			skippedAbsolute++
			continue
		}
		// Gate 2: adaptive z-score. Cuts high-floor-but-undifferentiated
		// candidates (cluster of similar scores where nothing truly
		// stands out). Zero ZScoreThreshold or zero stddev disables.
		if e.cfg.ZScoreThreshold > 0 && batchStddev > 0 {
			z := (c.fused - batchMean) / batchStddev
			if z < e.cfg.ZScoreThreshold {
				skippedZScore++
				continue
			}
		}
		edgeCount++
		sumEdge += c.fused
		if c.fused > maxEdge {
			maxEdge = c.fused
		}
		edge := &pb.Edge{
			FromMessageId:     c.prior.Id,
			ToMessageId:       msg.Id,
			Score:             float32(c.fused),
			Source:            pb.EdgeSource_EDGE_SOURCE_CROSS_ENCODER,
			CrossEncoderScore: float32(c.ce),
			TemporalProximity: float32(c.temporal),
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
	log.Printf("RRC: OnMessage target=%s thread=%s corpus=%d priors=%d skipped=%d newChunks=%d priorChunks=%d cached=%d reranked=%d candidates=%d mean=%.3f stddev=%.3f gated(abs=%d,z=%d) edgesNew=%d maxCE=%.3f avgCE=%.3f dur=%v",
		msg.Id, msg.ThreadId,
		len(corpus), len(priors), priorsSkipped,
		len(newChunks), len(priorChunks),
		totalCached, totalReranked,
		len(candidates), batchMean, batchStddev,
		skippedAbsolute, skippedZScore,
		len(edges), maxEdge, avgEdge, time.Since(t0))
	return edges, nil
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

// ApplyMMR reranks a Selected slice with Maximal Marginal Relevance
// (Carbonell & Goldstein 1998). The highest-score candidate keeps its
// slot; each subsequent candidate's effective score is replaced with
//
//	effective(C) = λ · origScore(C) - (1-λ) · max_sim(C, kept)
//
// where sim is cosine similarity between the candidates' representative
// vectors (mean-pool over chunks). Greedy pick-max-effective, repeat.
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
// Requires the ChunkOracle to be wired (same path as OnMessage's
// vector prefilter); without it, returns selected unchanged with an
// error. λ outside [0,1] or zero-length selected is a no-op.
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

	ids := make([]string, 0, len(selected))
	for _, s := range selected {
		ids = append(ids, s.MessageId)
	}
	chunkMap, err := e.oracle.ChunksForMessages(ctx, ids)
	if err != nil {
		return selected, fmt.Errorf("ApplyMMR: load chunks: %w", err)
	}

	// Representative vector per candidate = mean-pool over its chunks'
	// vectors. Chunks without a cached vector are fetched live via
	// EnsureVector — MMR needs every candidate to have a comparable
	// vector, and a missing vector would silently exclude that
	// candidate from the diversity penalty (it'd compare as zero-
	// similarity against everything, inflating its effective score).
	repVecs := make(map[string][]float32, len(ids))
	for _, id := range ids {
		chunks := chunkMap[id]
		if len(chunks) == 0 {
			continue
		}
		var sumVec []float32
		var n int
		for i := range chunks {
			v := chunks[i].Vector
			if v == nil {
				live, verr := e.oracle.EnsureVector(ctx, chunks[i])
				if verr != nil || live == nil {
					continue
				}
				v = live
			}
			if sumVec == nil {
				sumVec = make([]float32, len(v))
			}
			if len(v) != len(sumVec) {
				continue
			}
			for k, x := range v {
				sumVec[k] += x
			}
			n++
		}
		if n > 0 {
			for k := range sumVec {
				sumVec[k] /= float32(n)
			}
			repVecs[id] = sumVec
		}
	}

	// Greedy MMR. Track original scores so the tradeoff stays stable
	// (each pick re-reads origScore; the EffectiveScore we write back
	// is the MMR-adjusted value).
	orig := make(map[string]float64, len(selected))
	for _, s := range selected {
		orig[s.MessageId] = float64(s.EffectiveScore)
	}

	// Copy each SelectedMessage before rewriting EffectiveScore so
	// callers whose original slice escaped elsewhere (logging,
	// introspection publish, audit trails) don't observe a surprise
	// mutation. Pointer identity within out is stable for the MMR pass
	// itself; callers get fresh structs with adjusted scores.
	remaining := make([]*pb.SelectedMessage, len(selected))
	for i, s := range selected {
		cp := *s
		remaining[i] = &cp
	}
	sort.SliceStable(remaining, func(i, j int) bool {
		return orig[remaining[i].MessageId] > orig[remaining[j].MessageId]
	})

	out := make([]*pb.SelectedMessage, 0, len(selected))
	out = append(out, remaining[0])
	remaining = remaining[1:]

	for len(remaining) > 0 {
		bestIdx := -1
		bestScore := math.Inf(-1)
		for i, cand := range remaining {
			candVec := repVecs[cand.MessageId]
			var maxSim float64
			for _, kept := range out {
				if candVec == nil {
					continue
				}
				keptVec := repVecs[kept.MessageId]
				if keptVec == nil {
					continue
				}
				s := cosine(candVec, keptVec)
				if s > maxSim {
					maxSim = s
				}
			}
			effective := lambda*orig[cand.MessageId] - (1.0-lambda)*maxSim
			if effective > bestScore {
				bestScore = effective
				bestIdx = i
			}
		}
		pick := remaining[bestIdx]
		pick.EffectiveScore = float32(bestScore)
		out = append(out, pick)
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}
	return out, nil
}

// Fork creates an ephemeral engine for a forked thread. Inherits a
// snapshot of the parent's DAG and score cache.
func (e *Engine) Fork(_ string) (*Engine, error) {
	fork := &Engine{
		classifier: e.classifier,
		dag:        newDAG(),
		scores:     newScoreCache(),
		cfg:        e.cfg,
		oracle:     e.oracle,
	}
	for _, edge := range e.dag.AllEdges() {
		fork.dag.AddEdge(edge)
	}
	for k, v := range e.scores.all() {
		fork.scores.loadSilent(k, v)
	}
	return fork, nil
}

// Merge integrates a fork's edges into the parent. New scores from the
// fork flow through the parent's persister; inherited ones don't
// double-write because they're already cached (get-ok path in set).
func (e *Engine) Merge(fork *Engine, _ string) error {
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

// LoadDAG loads persisted edges into the engine (service calls on startup).
func (e *Engine) LoadDAG(edges []*pb.Edge) {
	for _, edge := range edges {
		e.dag.AddEdge(edge)
	}
}

// LoadScores hydrates the in-memory chunk-pair score cache from
// persisted rows. Replaces the previous LoadScoreCache(map) signature
// that exposed the internal scoreKey type. Silent path: no
// write-back to the DB.
func (e *Engine) LoadScores(scores []PersistedScore) {
	for _, ps := range scores {
		e.scores.loadSilent(scoreKey{
			FromMsgID:    ps.FromMsgID,
			FromChunkIdx: ps.FromChunkIdx,
			ToMsgID:      ps.ToMsgID,
			ToChunkIdx:   ps.ToChunkIdx,
		}, ps.Score)
	}
}

// --- internal helpers ---

// textFromMessage delegates to TextFromBlocks. Kept as a tiny shim so
// existing internal call sites (the empty-text filter in OnMessage)
// don't all need updating; it has no other purpose.
func textFromMessage(msg *pb.Message) string { return TextFromBlocks(msg.Content) }

// cosine is the standard cosine similarity; returns 0 for
// zero-length or mismatched vectors.
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
