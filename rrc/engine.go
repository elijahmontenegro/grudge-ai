package rrc

import (
	"context"
	"fmt"
	"sync"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
)

// Engine owns the prerequisite DAG, fingerprinted score cache, and
// immutable runtime configuration. Assemble serializes mutation of the
// DAG and cache while allowing independently constructed engines to run
// concurrently.
type Engine struct {
	mu     sync.Mutex
	scorer Scorer
	dag    *dag
	scores *scoreCache
	cfg    EngineConfig
	oracle ChunkOracle
}

type ChunkRef struct {
	MessageID      string
	ChunkIndex     int
	Text           string
	Vector         []float32
	RetrievalScore float64
}

type ChunkOracle interface {
	ChunksForMessages(ctx context.Context, messageIDs []string) (map[string][]ChunkRef, error)
	EnsureVector(ctx context.Context, ref ChunkRef) ([]float32, error)
	NearestChunks(ctx context.Context, queryText string, k int, predicate Predicate) ([]ChunkRef, error)
	DiversityRerank(ctx context.Context, candidates []*pb.SelectedMessage, originalScores map[string]float64, lambda float64) ([]*pb.SelectedMessage, error)
}

type Option func(*Engine)

func WithChunkOracle(oracle ChunkOracle) Option {
	return func(engine *Engine) { engine.oracle = oracle }
}

func WithScorePersister(persister ScorePersister) Option {
	return func(engine *Engine) {
		if engine.scores != nil {
			engine.scores.setPersister(persister)
		}
	}
}

func WithLoadedEdges(edges []*pb.Edge) Option {
	return func(engine *Engine) {
		for _, edge := range edges {
			engine.dag.AddEdge(edge)
		}
	}
}

func WithLoadedScores(scores []PersistedScore) Option {
	return func(engine *Engine) {
		for _, persisted := range scores {
			engine.scores.loadSilent(scoreKey{
				LocalContextFingerprint: persisted.LocalContextFingerprint,
				LocalContextChunkIndex:  persisted.LocalContextChunkIndex,
				CandidateMsgID:          persisted.CandidateMsgID,
				CandidateChunkIdx:       persisted.CandidateChunkIdx,
			}, persisted.Score)
		}
	}
}

func NewEngine(cfg EngineConfig, scorer Scorer, options ...Option) *Engine {
	engine := &Engine{
		scorer: scorer,
		dag:    newDAG(),
		scores: newScoreCache(),
		cfg:    cfg,
	}
	for _, option := range options {
		option(engine)
	}
	return engine
}

func (e *Engine) Config() EngineConfig { return e.cfg }

// Select walks prerequisite edges backward from the latest stored event.
func (e *Engine) Select(anchorID string, scope pb.SelectionScope, threadID string) (*pb.SelectionResult, error) {
	if scope == pb.SelectionScope_SELECTION_SCOPE_THREAD && threadID == "" {
		return nil, ErrThreadNotFound
	}

	selected, belowFloor := extractSubgraph(e.dag, anchorID, threadID, scope, e.cfg)
	selected = transitiveReduction(selected)
	result := &pb.SelectionResult{
		EventId:         "sel-" + anchorID,
		Scope:           scope,
		ThreadId:        threadID,
		AnchorMessageId: anchorID,
	}

	selectedIDs := make(map[string]bool)
	for _, item := range selected {
		selectedIDs[item.MessageID] = true
		result.Selected = append(result.Selected, &pb.SelectedMessage{
			MessageId: item.MessageID, EffectiveScore: float32(item.EffectiveScore),
			HopDepth: int32(item.HopDepth), ViaEdges: item.ViaEdges,
			ThreadId: item.ThreadID, CrossThread: item.CrossThread,
		})
	}
	for messageID, score := range belowFloor {
		if !selectedIDs[messageID] {
			result.Excluded = append(result.Excluded, &pb.ExcludedMessage{
				MessageId: messageID,
				Reason:    pb.ExclusionReason_EXCLUSION_REASON_BELOW_THRESHOLD,
				Score:     float32(score),
			})
		}
	}
	for _, edge := range e.dag.Prerequisites(anchorID) {
		if selectedIDs[edge.FromMessageId] || belowFloor[edge.FromMessageId] > 0 {
			continue
		}
		if !scopeAllows(edge, threadID, scope) {
			result.Excluded = append(result.Excluded, &pb.ExcludedMessage{
				MessageId: edge.FromMessageId,
				Reason:    pb.ExclusionReason_EXCLUSION_REASON_UNSPECIFIED,
				Score:     edge.Score,
			})
		}
	}
	return result, nil
}

func (e *Engine) ApplyMMR(ctx context.Context, selected []*pb.SelectedMessage, lambda float64) ([]*pb.SelectedMessage, error) {
	if len(selected) <= 1 || lambda <= 0 || lambda >= 1 {
		return selected, nil
	}
	if e.oracle == nil {
		return selected, fmt.Errorf("ApplyMMR: no chunk oracle configured")
	}
	original := make(map[string]float64, len(selected))
	for _, item := range selected {
		original[item.MessageId] = float64(item.EffectiveScore)
	}
	return e.oracle.DiversityRerank(ctx, selected, original, lambda)
}

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
	for key, score := range e.scores.all() {
		fork.scores.loadSilent(key, score)
	}
	return fork
}

func (e *Engine) Merge(fork *Engine) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, edge := range fork.dag.AllEdges() {
		e.dag.AddEdge(edge)
	}
	for key, score := range fork.scores.all() {
		if key.LocalContextFingerprint == "" {
			continue
		}
		if _, exists := e.scores.getLocalContext(
			key.LocalContextFingerprint, key.LocalContextChunkIndex,
			key.CandidateMsgID, key.CandidateChunkIdx,
		); !exists {
			e.scores.setLocalContext(
				key.LocalContextFingerprint, key.LocalContextChunkIndex,
				key.CandidateMsgID, key.CandidateChunkIdx, score,
			)
		}
	}
	return nil
}

func textFromMessage(message *pb.Message) string {
	return SerializeMessageForScoring(message)
}
