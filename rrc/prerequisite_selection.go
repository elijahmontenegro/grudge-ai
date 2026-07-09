package rrc

import (
	"context"
	"fmt"
	"math"
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
	return e.selectPrerequisitesLocked(ctx, local, anchor, scope, threadID)
}

// selectPrerequisitesLocked is SelectPrerequisites' body. Caller holds e.mu.
// Candidate generation is the ANN oracle's job now, so it needs no corpus.
func (e *Engine) selectPrerequisitesLocked(ctx context.Context, local *SerializedLocalContext, anchor *threadv1.Message, scope threadv1.SelectionScope, threadID string) ([]*rrcv1.Edge, PrerequisiteSelectionTelemetry, error) {
	if local == nil || len(local.Chunks) == 0 {
		return nil, PrerequisiteSelectionTelemetry{}, nil
	}
	if anchor == nil {
		return nil, PrerequisiteSelectionTelemetry{}, fmt.Errorf("SelectPrerequisites: nil anchor")
	}
	if e.oracle == nil {
		return nil, PrerequisiteSelectionTelemetry{}, fmt.Errorf("%w: no chunk oracle configured", ErrScorerUnavailable)
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
			return nil, PrerequisiteSelectionTelemetry{}, fmt.Errorf("nearest chunks: %w", err)
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
					return nil, PrerequisiteSelectionTelemetry{}, fmt.Errorf("%w: %v", ErrScorerFailed, err)
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
		// Value-stop under a binding budget: below massFloor, no
		// candidate can clear priced acceptance even at sim = 1, so
		// scoring a reach-only candidate under it is provably wasted
		// scorer compute. Sound because the floor tests EXACT mass
		// (post-walk), never the best-path bound. μ = 0 (slack) makes
		// the floor non-positive — vacuous by complementary slackness.
		massFloor := e.massFloorUnderPrice(price)
		missing := make([]string, 0, len(reachMass))
		for id := range reachMass {
			if _, alreadyScored := bestScore[id]; alreadyScored {
				continue // top-K cosine already surfaced it
			}
			if reachMass[id] < massFloor {
				continue // sterile at the current price
			}
			// The walk already applied scope and excludes the cone (local
			// context), so a reached id needs no further eligibility check.
			missing = append(missing, id)
		}
		if len(missing) > 0 {
			provReached, err := e.scoreReachedMessages(ctx, local, missing, bestScore, bestTokens)
			if err != nil {
				return nil, PrerequisiteSelectionTelemetry{}, err
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
		}, nil
	}

	// Acceptance is calibrated expected value against the token budget's
	// marginal price (A4, complete). Each candidate's raw similarity
	// (candidate.score) and structural mass (reachMass) fuse through the
	// Calibrator into P(prereq); accept when the excess over the
	// precision stance covers the candidate's cost at the thread's
	// realized shadow price: P ≥ LossRatio + μ·tokens. μ comes from the
	// previous assembly's shed equilibrium — exactly zero under slack
	// (complementary slackness), the marginal refused density when the
	// budget bit — and tokens is the candidate's delivery-cost lower
	// bound. Selection is the price filter; the shed stays the hard
	// enforcer. The stored edge Score is the calibrated P — the
	// formation-time audit record (traversal beyond hop 1 derives from
	// CrossEncoderScore under the current calibrator; see selection.go).
	var edges []*rrcv1.Edge
	for _, candidate := range candidates {
		sim := candidate.score
		mass := reachMass[candidate.id]
		p := e.cfg.Calibrator.Predict(sim, mass)
		if !accept(p, e.cfg.LossRatio, price, bestTokens[candidate.id]) {
			continue
		}
		edge := &rrcv1.Edge{
			FromMessageId:     candidate.id,
			ToMessageId:       anchor.Id,
			Score:             float32(p),
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
	return edges, PrerequisiteSelectionTelemetry{
		DurationMs:          duration.Milliseconds(),
		PriorsConsidered:    len(candidates),
		CandidatesScored:    len(candidates),
		Reranked:            totalReranked,
		EdgesFormed:         len(edges),
		ProvenanceReached:   provenanceReached,
		ProvenanceTruncated: reachTruncated,
	}, nil
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

// massFloorUnderPrice: under shadow price μ, the mass below which NO
// candidate clears priced acceptance even at sim = 1 and minimal cost
// (t_min = 1 token): σ(A + B·m + C) < LossRatio + μ. A theorem of the
// fitted acceptance law, not a knob — it moves when the fit moves.
// μ = 0 (slack) yields a non-positive floor: vacuous by complementary
// slackness, which is why the walk's yield is only ever priced when the
// budget actually binds. A target ≥ 1 is unclearable at any mass.
func (e *Engine) massFloorUnderPrice(mu float64) float64 {
	if mu <= 0 {
		return 0
	}
	target := e.cfg.LossRatio + mu
	if target >= 1 {
		return math.Inf(1)
	}
	c := e.cfg.Calibrator
	if c.B <= 0 {
		return 0
	}
	return (math.Log(target/(1-target)) - c.A - c.C) / c.B
}
