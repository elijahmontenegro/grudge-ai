package rrc

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SelectPrerequisites scores serialized Local Context against eligible
// stored messages and attaches prerequisite edges to the latest stored event.
func (e *Engine) SelectPrerequisites(ctx context.Context, local *SerializedLocalContext, anchor *pb.Message, corpus []*pb.Message, scope pb.SelectionScope, threadID string) ([]*pb.Edge, PrerequisiteSelectionTelemetry, error) {
	if local == nil || len(local.Chunks) == 0 || len(corpus) == 0 {
		return nil, PrerequisiteSelectionTelemetry{}, nil
	}
	if anchor == nil {
		return nil, PrerequisiteSelectionTelemetry{}, fmt.Errorf("SelectPrerequisites: nil anchor")
	}
	if e.oracle == nil {
		return nil, PrerequisiteSelectionTelemetry{}, fmt.Errorf("%w: no chunk oracle configured", ErrScorerUnavailable)
	}

	started := time.Now()
	excluded := make(map[string]bool, len(local.MessageIDs))
	for _, id := range local.MessageIDs {
		excluded[id] = true
	}
	eligible := make(map[string]*pb.Message, len(corpus))
	for _, message := range corpus {
		if excluded[message.Id] || textFromMessage(message) == "" {
			continue
		}
		if scope == pb.SelectionScope_SELECTION_SCOPE_THREAD && message.ThreadId != threadID {
			continue
		}
		eligible[message.Id] = message
	}
	if len(eligible) == 0 {
		return nil, PrerequisiteSelectionTelemetry{DurationMs: time.Since(started).Milliseconds()}, nil
	}

	retrievalScope := ScopeAll
	if scope == pb.SelectionScope_SELECTION_SCOPE_THREAD {
		retrievalScope = ScopeThread
	}
	predicate := PredAnd{Children: []Predicate{
		PredScope{CurrentThread: threadID, Scope: retrievalScope},
		PredExcludeMessageIDs{MessageIDs: append([]string(nil), local.MessageIDs...)},
	}}

	bestScore := make(map[string]float64, len(eligible))
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
		filtered := make([]ChunkRef, 0, len(retrieved))
		for _, candidate := range retrieved {
			if _, ok := eligible[candidate.MessageID]; ok {
				filtered = append(filtered, candidate)
			}
		}
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
			}
		}
	}

	// Provenance-traversal recall. Top-K cosine amputates required-but-
	// low-similarity messages (roots above all) before they can be scored;
	// this union adds messages reachable via recorded provenance edges from
	// the active-discourse cone, fetches their chunks, and scores them
	// against the same Local Context so they compete on equal footing. This
	// is the mandatory recall change — without it the /\ acceptance boundary
	// has nothing to act on for roots (see structural-lift open-Q #3).
	reachMass, reachTruncated := e.provenanceReach(local.MessageIDs, threadID, scope)
	var provenanceReached int
	if len(reachMass) > 0 {
		missing := make([]string, 0, len(reachMass))
		for id := range reachMass {
			if _, alreadyScored := bestScore[id]; alreadyScored {
				continue // top-K cosine already surfaced it
			}
			if _, ok := eligible[id]; !ok {
				continue // excluded (local context / out of scope / empty)
			}
			missing = append(missing, id)
		}
		if len(missing) > 0 {
			provReached, err := e.scoreReachedMessages(ctx, local, missing, bestScore)
			if err != nil {
				return nil, PrerequisiteSelectionTelemetry{}, err
			}
			provenanceReached = provReached
			totalReranked += provReached
		}
	}

	type scoredMessage struct {
		message *pb.Message
		score   float64
	}
	candidates := make([]scoredMessage, 0, len(bestScore))
	for id, score := range bestScore {
		candidates = append(candidates, scoredMessage{message: eligible[id], score: score})
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
			PriorsConsidered: len(eligible),
			CandidatesScored: len(candidates),
			Reranked:         totalReranked,
		}, nil
	}

	// Acceptance is calibrated expected value, not a flat threshold (A4).
	// Each candidate's raw similarity (candidate.score) and structural mass
	// (reachMass) fuse through the Calibrator into P(prereq); accept when
	// expected value clears the marginal token price. At edge FORMATION the
	// shadow price μ is 0 (the token-scarcity cut happens later, in the
	// assembly shed loop where the budget binds), so the formation floor is
	// P(prereq) ≥ LossRatio — the precision stance. The stored edge Score is
	// the calibrated P so downstream traversal and budget ranking speak the
	// same currency; CrossEncoderScore retains the raw similarity.
	var edges []*pb.Edge
	for _, candidate := range candidates {
		sim := candidate.score
		mass := reachMass[candidate.message.Id]
		p := e.cfg.Calibrator.Predict(sim, mass)
		if !accept(p, e.cfg.LossRatio, 0 /* μ at formation */, 0 /* tokens n/a at formation */) {
			continue
		}
		edge := &pb.Edge{
			FromMessageId:     candidate.message.Id,
			ToMessageId:       anchor.Id,
			Score:             float32(p),
			Source:            pb.EdgeSource_EDGE_SOURCE_CROSS_ENCODER,
			CrossEncoderScore: float32(sim),
			DetectedAt:        timestamppb.Now(),
			FromThreadId:      candidate.message.ThreadId,
			ToThreadId:        anchor.ThreadId,
		}
		e.dag.AddEdge(edge)
		edges = append(edges, edge)
	}

	duration := time.Since(started)
	log.Printf("RRC: SelectPrerequisites fingerprint=%s anchor=%s thread=%s eligible=%d localContextChunks=%d retrieved=%d cached=%d reranked=%d edges=%d dur=%v",
		local.Fingerprint, anchor.Id, anchor.ThreadId, len(eligible), len(local.Chunks),
		totalRetrieved, totalCached, totalReranked, len(edges), duration)
	return edges, PrerequisiteSelectionTelemetry{
		DurationMs:          duration.Milliseconds(),
		PriorsConsidered:    len(eligible),
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
	// ProvenanceReached is how many candidates entered the pool via the
	// provenance-traversal recall path (not top-K cosine) and were scored.
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
func (e *Engine) scoreReachedMessages(ctx context.Context, local *SerializedLocalContext, messageIDs []string, bestScore map[string]float64) (int, error) {
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
				}
			}
		}
	}
	return scored, nil
}
