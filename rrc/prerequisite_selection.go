package rrc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SelectPrerequisites scores serialized Local Context against eligible
// stored messages and attaches prerequisite edges to the latest stored
// event. The mass walk seeds from the serialized MEMBERSHIP — the
// delivered window (preceding turn ∪ current turn), whose tail is the
// recorded graph's entry point for a fresh turn (its own messages carry
// no incoming provenance edges until it generates). Takes the engine
// mutex — safe for external callers alongside Assemble /
// RecordProvenance / Fork / Merge.
func (e *Engine) SelectPrerequisites(ctx context.Context, local *SerializedLocalContext, anchor *threadv1.Message, scope threadv1.SelectionScope, threadID string) ([]*rrcv1.Edge, PrerequisiteSelectionTelemetry, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	edges, tel, _, err := e.selectPrerequisitesLocked(ctx, local, anchor, scope, threadID)
	return edges, tel, err
}

// selectPrerequisitesLocked is SelectPrerequisites' body. Caller holds e.mu.
// Candidate generation is the ANN oracle's job now, so it needs no corpus.
// selectPrerequisitesLocked also returns the event's measured noise
// floor (nil when ungated) so the same event's traversal can interpret
// stored observations with it — the floor never outlives the event.
func (e *Engine) selectPrerequisitesLocked(ctx context.Context, local *SerializedLocalContext, anchor *threadv1.Message, scope threadv1.SelectionScope, threadID string) ([]*rrcv1.Edge, PrerequisiteSelectionTelemetry, []float64, error) {
	if local == nil || len(local.Chunks) == 0 {
		return nil, PrerequisiteSelectionTelemetry{}, nil, nil
	}
	if anchor == nil {
		return nil, PrerequisiteSelectionTelemetry{}, nil, fmt.Errorf("SelectPrerequisites: nil anchor")
	}
	if e.oracle == nil {
		return nil, PrerequisiteSelectionTelemetry{}, nil, fmt.Errorf("%w: no chunk oracle configured", ErrScorerUnavailable)
	}

	started := time.Now()
	// The oracle applies this predicate (thread scope + local-context
	// exclusion), so candidates come back already filtered — no full-corpus
	// eligibility scan, and no dependence on the corpus argument at all.
	retrievalScope := ScopeAll
	if scope == threadv1.SelectionScope_SELECTION_SCOPE_THREAD {
		retrievalScope = ScopeThread
	}
	predicate := PredAnd{Children: []Predicate{
		PredScope{CurrentThread: threadID, Scope: retrievalScope},
		PredExcludeMessageIDs{MessageIDs: append([]string(nil), local.MessageIDs...)},
	}}

	// The thread's realized budget shadow price from the previous
	// assembly equilibrium — complementary slackness makes it exactly
	// zero under slack, so the μ·tokens acceptance term is dormant by
	// law whenever the budget doesn't bind.
	price := e.lastPrice(threadID)

	bestScore := make(map[string]float64)
	// bestTokens is each candidate's delivery-cost LOWER BOUND — the
	// token estimate of its best-scoring chunk. Selection is the price
	// FILTER (charge at least this); the assembly shed is the hard
	// enforcer with exact wire costs, so an underestimate here can never
	// overrun the budget.
	bestTokens := make(map[string]int)
	// threadByID maps a candidate message id to its thread, for edge
	// construction — filled from the candidates' own ThreadID (retrieval) and
	// the provenance walk (reach), never a corpus scan.
	threadByID := make(map[string]string)
	var totalCached, totalReranked, totalRetrieved int
	for _, localChunk := range local.Chunks {
		k := e.cfg.RerankTopK
		if k <= 0 {
			k = 64
		}
		retrieved, err := e.oracle.NearestChunks(ctx, localChunk.Text, k, predicate)
		if err != nil {
			return nil, PrerequisiteSelectionTelemetry{}, nil, fmt.Errorf("nearest chunks: %w", err)
		}
		// Candidates are already scope-filtered and local-excluded by the
		// predicate; record each one's thread for its edge.
		for _, candidate := range retrieved {
			threadByID[candidate.MessageID] = candidate.ThreadID
		}
		filtered := retrieved
		totalRetrieved += len(filtered)

		type scoredCandidate struct {
			ref   ChunkRef
			score float64
		}
		candidates := make([]scoredCandidate, len(filtered))
		var uncached []int
		for i, candidate := range filtered {
			if score, ok := e.scores.getLocalContext(local.Fingerprint, localChunk.Index, candidate.MessageID, candidate.ChunkIndex); ok {
				candidates[i] = scoredCandidate{ref: candidate, score: score}
			} else {
				candidates[i] = scoredCandidate{ref: candidate}
				uncached = append(uncached, i)
			}
		}
		totalCached += len(filtered) - len(uncached)

		if len(uncached) > 0 {
			if e.scorer != nil {
				texts := make([]string, len(uncached))
				for i, idx := range uncached {
					texts[i] = candidates[idx].ref.Text
				}
				scores, err := e.scorer.Score(ctx, localChunk.Text, texts)
				if err != nil {
					return nil, PrerequisiteSelectionTelemetry{}, nil, fmt.Errorf("%w: %v", ErrScorerFailed, err)
				}
				for i, idx := range uncached {
					var score float64
					if i < len(scores) {
						score = scores[i]
					}
					candidates[idx].score = score
					e.scores.setLocalContext(local.Fingerprint, localChunk.Index, candidates[idx].ref.MessageID, candidates[idx].ref.ChunkIndex, score)
				}
				totalReranked += len(uncached)
			} else {
				for _, idx := range uncached {
					score := candidates[idx].ref.RetrievalScore
					candidates[idx].score = score
					e.scores.setLocalContext(local.Fingerprint, localChunk.Index, candidates[idx].ref.MessageID, candidates[idx].ref.ChunkIndex, score)
				}
			}
		}

		for _, candidate := range candidates {
			current, exists := bestScore[candidate.ref.MessageID]
			if !exists || candidate.score > current {
				bestScore[candidate.ref.MessageID] = candidate.score
				bestTokens[candidate.ref.MessageID] = e.cfg.Chunk.Estimate(candidate.ref.Text)
			}
		}
	}

	// Provenance-traversal recall. Top-K cosine amputates required-but-
	// low-similarity messages (roots above all) before they can be scored;
	// this union adds messages reachable via recorded provenance edges,
	// fetches their chunks, and scores them against the same Local Context
	// so they compete on equal footing. This is the mandatory recall change
	// — without it the /\ acceptance boundary has nothing to act on for
	// roots (see structural-lift open-Q #3).
	//
	// The walk's anchor set is the serialized MEMBERSHIP: the delivered
	// window, preceding turn included. A fresh turn's own messages have no
	// incoming provenance edges (recorded contributor → anchor at
	// generation time), so the window-tail is the walk's entry into the
	// recorded graph — without it the walk finds nothing at any trigger
	// call and mass could neither act nor calibrate. Seeds never surface
	// as candidates (they are delivered — the exclusion predicate above is
	// the same id set); what surfaces is their undelivered ancestry, at
	// its banked mass.
	walkAnchors := local.MessageIDs
	reachMass, reachThreads, reachTruncated := e.provenanceReach(walkAnchors, threadID, scope)
	var provenanceReached int
	if len(reachMass) > 0 {
		missing := make([]string, 0, len(reachMass))
		for id := range reachMass {
			if _, alreadyScored := bestScore[id]; alreadyScored {
				continue // top-K cosine already surfaced it
			}
			// The walk already applied scope and excludes the cone (local
			// context), so a reached id needs no further eligibility check.
			missing = append(missing, id)
		}
		if len(missing) > 0 {
			provReached, err := e.scoreReachedMessages(ctx, local, missing, bestScore, bestTokens)
			if err != nil {
				return nil, PrerequisiteSelectionTelemetry{}, nil, err
			}
			provenanceReached = provReached
			totalReranked += provReached
		}
		for id, t := range reachThreads {
			if _, ok := threadByID[id]; !ok {
				threadByID[id] = t
			}
		}
	}

	type scoredMessage struct {
		id    string
		score float64
	}
	candidates := make([]scoredMessage, 0, len(bestScore))
	for id, score := range bestScore {
		candidates = append(candidates, scoredMessage{id: id, score: score})
	}

	var mean, stddev float64
	for _, candidate := range candidates {
		mean += candidate.score
	}
	if len(candidates) > 0 {
		mean /= float64(len(candidates))
		for _, candidate := range candidates {
			delta := candidate.score - mean
			stddev += delta * delta
		}
		stddev = math.Sqrt(stddev / float64(len(candidates)))
	}
	if e.scorer != nil && e.cfg.MinBatchStdDev > 0 && len(candidates) > 0 && stddev < e.cfg.MinBatchStdDev {
		return nil, PrerequisiteSelectionTelemetry{
			DurationMs:       time.Since(started).Milliseconds(),
			PriorsConsidered: len(candidates),
			CandidatesScored: len(candidates),
			Reranked:         totalReranked,
		}, nil, nil
	}

	// Acceptance is DETECTION against a noise floor the event measures
	// about itself. The reference sample — R corpus chunks drawn
	// deterministically for this event, scored against the same query
	// with the same max-merge — is the background distribution; a
	// candidate is accepted when it beats the WHOLE sample, whose exact
	// rank-based null probability is 1/(R+1) under exchangeability,
	// distribution-free, in any scorer's units. The stance s0 (LossRatio
	// in bits) and the budget's realized price μ (bits per wire token)
	// charge the detection: bits ≥ s0 + μ·tokens. With no measurable
	// floor (cold start: corpus smaller than the minimum sample) the
	// event runs UNGATED — the budget alone arbitrates; tiny corpora fit
	// in budget anyway. The stored edge Score is the detection
	// confidence (1 − p), the formation-time audit record;
	// CrossEncoderScore keeps the raw observation.
	var refScores []float64
	if len(candidates) > 0 {
		floor, ferr := e.referenceFloor(ctx, local, predicate)
		if ferr != nil {
			return nil, PrerequisiteSelectionTelemetry{}, nil, ferr
		}
		refScores = floor
	}
	gated := len(refScores) >= minReferenceSample
	if gated && flatReference(refScores) {
		return nil, PrerequisiteSelectionTelemetry{}, nil, fmt.Errorf("%w: reference sample is flat — the scoring instrument is not discriminating", ErrScorerFailed)
	}
	s0 := stanceBits(e.cfg.LossRatio)
	beatAll := 1.0 / float64(len(refScores)+1)
	// The mass channel is an INDEPENDENT detector — never summed with
	// similarity through an exchange rate. Its null population is the
	// walk's reached set itself: a candidate's mass rank among this
	// turn's structural ancestry gives an exact rank-based null
	// probability, so a heavy hitter surfaces on structural evidence
	// alone (junk similarity included — the dropped-then-recalled
	// regime), gated by the same stance and price in the same bits.
	// Detections that pass BOTH channels add their bits (the channels
	// are measured independent) — richer joint evidence, higher audit
	// confidence.
	massBits := make(map[string]float64, len(reachMass))
	if n := len(reachMass); n > 0 {
		ranked := make([]string, 0, n)
		for id := range reachMass {
			ranked = append(ranked, id)
		}
		sort.Slice(ranked, func(i, j int) bool {
			if reachMass[ranked[i]] != reachMass[ranked[j]] {
				return reachMass[ranked[i]] > reachMass[ranked[j]]
			}
			return ranked[i] < ranked[j]
		})
		for i, id := range ranked {
			massBits[id] = surprisalBits(float64(i+1) / float64(n+1))
		}
	}
	var edges []*rrcv1.Edge
	for _, candidate := range candidates {
		sim := candidate.score
		priceBits := s0 + price*float64(bestTokens[candidate.id])
		var conf float64
		if gated {
			semBits := 0.0
			if p := nullP(sim, refScores); p <= beatAll {
				semBits = surprisalBits(p)
			}
			mBits := massBits[candidate.id]
			semDetected := semBits > 0 && semBits >= priceBits
			massDetected := mBits > 0 && mBits >= priceBits
			if !semDetected && !massDetected {
				continue // stands out in neither channel at this price
			}
			total := 0.0
			if semDetected {
				total += semBits
			}
			if massDetected {
				total += mBits
			}
			conf = confidenceFromBits(total)
		} else {
			conf = sim
			if conf < 0 {
				conf = 0
			} else if conf > 1 {
				conf = 1
			}
		}
		edge := &rrcv1.Edge{
			FromMessageId:     candidate.id,
			ToMessageId:       anchor.Id,
			Score:             float32(conf),
			Source:            rrcv1.EdgeSource_EDGE_SOURCE_CROSS_ENCODER,
			CrossEncoderScore: float32(sim),
			DetectedAt:        timestamppb.Now(),
			FromThreadId:      threadByID[candidate.id],
			ToThreadId:        anchor.ThreadId,
			ScorerModel:       e.cfg.ScorerModelID,
		}
		if !e.admitEdge(edge) {
			continue
		}
		edges = append(edges, edge)
	}

	duration := time.Since(started)
	e.logger.Info("RRC: SelectPrerequisites",
		"fingerprint", local.Fingerprint, "anchor", anchor.Id, "thread", anchor.ThreadId,
		"candidates", len(candidates), "localContextChunks", len(local.Chunks),
		"retrieved", totalRetrieved, "cached", totalCached, "reranked", totalReranked,
		"edges", len(edges), "dur", duration)
	var eventFloor []float64
	if gated {
		eventFloor = refScores
	}
	return edges, PrerequisiteSelectionTelemetry{
		DurationMs:          duration.Milliseconds(),
		PriorsConsidered:    len(candidates),
		CandidatesScored:    len(candidates),
		Reranked:            totalReranked,
		EdgesFormed:         len(edges),
		ProvenanceReached:   provenanceReached,
		ProvenanceTruncated: reachTruncated,
	}, eventFloor, nil
}

type PrerequisiteSelectionTelemetry struct {
	DurationMs       int64
	PriorsConsidered int
	CandidatesScored int
	Reranked         int
	EdgesFormed      int
	// ProvenanceReached is how many (local chunk, candidate chunk) pairs were
	// scored via the provenance-traversal recall path — reached messages not
	// already surfaced by top-K cosine, each scored against every Local
	// Context chunk (the same max-merge footing as the cosine path).
	// ProvenanceTruncated reports the walk hit provenanceReachCap — a
	// visible signal so a bounded reach is never mistaken for full coverage.
	ProvenanceReached   int
	ProvenanceTruncated bool
}

// scoreReachedMessages fetches chunks for provenance-reached messages and
// scores each against the Local Context, merging the max per message into
// bestScore (same max-merge as the cosine path) so reached candidates
// compete on identical footing. Returns the number of (chunk-pair) scores
// computed. Scores are cached like the cosine path. Uncached-only: a reached
// message whose pair was already scored this fingerprint reuses the cache.
func (e *Engine) scoreReachedMessages(ctx context.Context, local *SerializedLocalContext, messageIDs []string, bestScore map[string]float64, bestTokens map[string]int) (int, error) {
	chunksByMsg, err := e.oracle.ChunksForMessages(ctx, messageIDs)
	if err != nil {
		return 0, fmt.Errorf("provenance reach: chunks for messages: %w", err)
	}
	var scored int
	for _, localChunk := range local.Chunks {
		for msgID, chunks := range chunksByMsg {
			for _, cand := range chunks {
				if s, ok := e.scores.getLocalContext(local.Fingerprint, localChunk.Index, msgID, cand.ChunkIndex); ok {
					if cur, exists := bestScore[msgID]; !exists || s > cur {
						bestScore[msgID] = s
						bestTokens[msgID] = e.cfg.Chunk.Estimate(cand.Text)
					}
					continue
				}
				var score float64
				if e.scorer != nil {
					scores, err := e.scorer.Score(ctx, localChunk.Text, []string{cand.Text})
					if err != nil {
						return scored, fmt.Errorf("%w: %v", ErrScorerFailed, err)
					}
					if len(scores) > 0 {
						score = scores[0]
					}
					scored++
				} else {
					score = cand.RetrievalScore
				}
				e.scores.setLocalContext(local.Fingerprint, localChunk.Index, msgID, cand.ChunkIndex, score)
				if cur, exists := bestScore[msgID]; !exists || score > cur {
					bestScore[msgID] = score
					bestTokens[msgID] = e.cfg.Chunk.Estimate(cand.Text)
				}
			}
		}
	}
	return scored, nil
}

// referenceFloor measures the event's noise floor: the reference sample
// (R corpus chunks, deterministically drawn for this fingerprint,
// predicate-filtered exactly like candidates) scored against every query
// chunk with the same max-merge and the same cache as candidates.
// Returns one best-score per reference chunk. nil scorer or a corpus
// smaller than the draw returns what exists — the caller decides gated
// vs ungated by sample size.
func (e *Engine) referenceFloor(ctx context.Context, local *SerializedLocalContext, predicate Predicate) ([]float64, error) {
	if e.scorer == nil || e.oracle == nil {
		return nil, nil
	}
	refs, err := e.oracle.RandomChunks(ctx, ReferenceSampleSize, referenceSeed(local.Fingerprint), predicate)
	if err != nil {
		return nil, fmt.Errorf("reference draw: %w", err)
	}
	if len(refs) == 0 {
		return nil, nil
	}
	best := make([]float64, len(refs))
	for i := range best {
		best[i] = math.Inf(-1)
	}
	for _, localChunk := range local.Chunks {
		var uncached []int
		for i, ref := range refs {
			if sc, ok := e.scores.getLocalContext(local.Fingerprint, localChunk.Index, ref.MessageID, ref.ChunkIndex); ok {
				if sc > best[i] {
					best[i] = sc
				}
			} else {
				uncached = append(uncached, i)
			}
		}
		if len(uncached) == 0 {
			continue
		}
		texts := make([]string, len(uncached))
		for j, i := range uncached {
			texts[j] = refs[i].Text
		}
		scores, err := e.scorer.Score(ctx, localChunk.Text, texts)
		if err != nil {
			return nil, fmt.Errorf("%w: reference scoring: %v", ErrScorerFailed, err)
		}
		for j, i := range uncached {
			var sc float64
			if j < len(scores) {
				sc = scores[j]
			}
			e.scores.setLocalContext(local.Fingerprint, localChunk.Index, refs[i].MessageID, refs[i].ChunkIndex, sc)
			if sc > best[i] {
				best[i] = sc
			}
		}
	}
	return best, nil
}
