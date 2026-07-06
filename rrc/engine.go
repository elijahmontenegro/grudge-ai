package rrc

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"

	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"google.golang.org/protobuf/proto"
)

// Engine owns the prerequisite DAG, fingerprinted score cache, and
// immutable runtime configuration. Assemble serializes mutation of the
// DAG and cache while allowing independently constructed engines to run
// concurrently.
type Engine struct {
	mu         sync.Mutex
	scorer     Scorer
	dag        *dag
	scores     *scoreCache
	cfg        EngineConfig
	oracle     ChunkOracle
	edgeFilter func(*rrcv1.Edge) bool
	logger     *slog.Logger
}

type ChunkRef struct {
	MessageID      string
	ChunkIndex     int
	ThreadID       string
	Text           string
	Vector         []float32
	RetrievalScore float64
}

type ChunkOracle interface {
	ChunksForMessages(ctx context.Context, messageIDs []string) (map[string][]ChunkRef, error)
	EnsureVector(ctx context.Context, ref ChunkRef) ([]float32, error)
	NearestChunks(ctx context.Context, queryText string, k int, predicate Predicate) ([]ChunkRef, error)
	// RepresentativeVectors returns one comparable vector per message
	// (mean-pooled chunk vectors for single-vector backends; multi-vector
	// backends substitute their own representation). Messages without a
	// representation are simply absent from the map. This is the only
	// backend-specific piece of MMR — the greedy loop itself lives on the
	// engine (ApplyMMR).
	RepresentativeVectors(ctx context.Context, messageIDs []string) (map[string][]float32, error)
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

func WithLoadedEdges(edges []*rrcv1.Edge) Option {
	return func(engine *Engine) {
		for _, edge := range edges {
			engine.dag.AddEdge(edge)
		}
	}
}

// WithEdgeFilter installs an interception seam on edge formation: the
// filter runs on every edge the engine is about to admit (cross-encoder
// formation and provenance recording — not hydration via
// WithLoadedEdges, which replays edges a filter already saw). It may
// mutate the edge in place; returning false drops it. The consumer
// adjusts or vetoes edges before they enter the DAG.
func WithEdgeFilter(filter func(*rrcv1.Edge) bool) Option {
	return func(engine *Engine) { engine.edgeFilter = filter }
}

// WithLogger routes the engine's telemetry lines (selection summaries,
// MMR skips) through the host's logger instead of slog.Default(). An
// importing application controls its own log surface.
func WithLogger(l *slog.Logger) Option {
	return func(engine *Engine) {
		if l != nil {
			engine.logger = l
		}
	}
}

// admitEdge applies the edge filter (when installed) and adds the edge
// to the DAG. Caller holds e.mu. Returns false when the filter dropped
// the edge.
func (e *Engine) admitEdge(edge *rrcv1.Edge) bool {
	if e.edgeFilter != nil && !e.edgeFilter(edge) {
		return false
	}
	e.dag.AddEdge(edge)
	return true
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

// NewEngine constructs an engine. cfg.Chunk.Estimator is a constructor
// invariant: the engine speaks token units on every Assemble
// (Local Context serialization chunks, and wire sizing runs before the
// budget check, so no budget setting makes the estimator optional).
// Constructing an engine without a tokenizer is a programming error,
// surfaced here rather than mid-flight on the first message.
func NewEngine(cfg EngineConfig, scorer Scorer, options ...Option) *Engine {
	if cfg.Chunk.Estimator == nil {
		panic("rrc: EngineConfig.Chunk.Estimator is nil — wire a TokenEstimator at construction (e.g. rrc/tiktoken)")
	}
	engine := &Engine{
		scorer: scorer,
		dag:    newDAG(),
		scores: newScoreCache(),
		cfg:    cfg,
		logger: slog.Default(),
	}
	for _, option := range options {
		option(engine)
	}
	return engine
}

func (e *Engine) Config() EngineConfig { return e.cfg }

// Select walks prerequisite edges backward from the latest stored event.
// Takes the engine mutex — safe for external callers alongside
// Assemble / RecordProvenance / Fork / Merge.
func (e *Engine) Select(anchorID string, scope threadv1.SelectionScope, threadID string) (*rrcv1.SelectionResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.selectLocked(anchorID, scope, threadID)
}

// selectLocked is Select's body. Caller holds e.mu.
func (e *Engine) selectLocked(anchorID string, scope threadv1.SelectionScope, threadID string) (*rrcv1.SelectionResult, error) {
	if scope == threadv1.SelectionScope_SELECTION_SCOPE_THREAD && threadID == "" {
		return nil, ErrThreadNotFound
	}

	selected, belowFloor := extractSubgraph(e.dag, anchorID, threadID, scope, e.cfg)
	selected = transitiveReduction(selected)
	result := &rrcv1.SelectionResult{
		EventId:         "sel-" + anchorID,
		Scope:           scope,
		ThreadId:        threadID,
		AnchorMessageId: anchorID,
	}

	selectedIDs := make(map[string]bool)
	for _, item := range selected {
		selectedIDs[item.MessageID] = true
		result.Selected = append(result.Selected, &rrcv1.SelectedMessage{
			MessageId: item.MessageID, EffectiveScore: float32(item.EffectiveScore),
			HopDepth: int32(item.HopDepth), ViaEdges: item.ViaEdges,
			ThreadId: item.ThreadID, CrossThread: item.CrossThread,
		})
	}
	for messageID, score := range belowFloor {
		if !selectedIDs[messageID] {
			result.Excluded = append(result.Excluded, &rrcv1.ExcludedMessage{
				MessageId: messageID,
				Reason:    rrcv1.ExclusionReason_EXCLUSION_REASON_BELOW_THRESHOLD,
				Score:     float32(score),
			})
		}
	}
	for _, edge := range e.dag.Prerequisites(anchorID) {
		if selectedIDs[edge.FromMessageId] || belowFloor[edge.FromMessageId] > 0 {
			continue
		}
		if !scopeAllows(edge, threadID, scope) {
			result.Excluded = append(result.Excluded, &rrcv1.ExcludedMessage{
				MessageId: edge.FromMessageId,
				Reason:    rrcv1.ExclusionReason_EXCLUSION_REASON_UNSPECIFIED,
				Score:     edge.Score,
			})
		}
	}
	return result, nil
}

// ApplyMMR reranks selected for diversity via MMR (Carbonell &
// Goldstein 1998): effective(C) = λ·orig(C) − (1−λ)·max_sim(C, kept),
// greedy pick-max-effective, first pick the highest-original-score
// candidate. The oracle supplies only the per-message representative
// vectors; the algorithm is engine-owned. When the oracle has no
// representation for any candidate the input is returned unchanged —
// an unmeasurable diversity penalty must not silently rescale scores.
func (e *Engine) ApplyMMR(ctx context.Context, selected []*rrcv1.SelectedMessage, lambda float64) ([]*rrcv1.SelectedMessage, error) {
	if len(selected) <= 1 || lambda <= 0 || lambda >= 1 {
		return selected, nil
	}
	if e.oracle == nil {
		return selected, fmt.Errorf("ApplyMMR: no chunk oracle configured")
	}
	original := make(map[string]float64, len(selected))
	ids := make([]string, 0, len(selected))
	for _, item := range selected {
		original[item.MessageId] = float64(item.EffectiveScore)
		ids = append(ids, item.MessageId)
	}
	repVecs, err := e.oracle.RepresentativeVectors(ctx, ids)
	if err != nil {
		return selected, fmt.Errorf("ApplyMMR: representative vectors: %w", err)
	}
	if len(repVecs) == 0 {
		return selected, nil
	}

	// Copy each SelectedMessage before rewriting EffectiveScore so
	// callers whose original slice escaped elsewhere (logging,
	// introspection publish, audit trails) don't observe a surprise
	// mutation. proto.Clone (not value-copy) because the proto type
	// embeds a MessageState containing a mutex.
	remaining := make([]*rrcv1.SelectedMessage, len(selected))
	for i, s := range selected {
		remaining[i] = proto.Clone(s).(*rrcv1.SelectedMessage)
	}
	sort.SliceStable(remaining, func(i, j int) bool {
		return original[remaining[i].MessageId] > original[remaining[j].MessageId]
	})

	out := make([]*rrcv1.SelectedMessage, 0, len(selected))
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
				if s := cosineSim(candVec, keptVec); s > maxSim {
					maxSim = s
				}
			}
			effective := lambda*original[cand.MessageId] - (1.0-lambda)*maxSim
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

// cosineSim is the standard cosine similarity; returns 0 for
// zero-length or mismatched vectors.
func cosineSim(a, b []float32) float64 {
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

func (e *Engine) Fork() *Engine {
	e.mu.Lock()
	defer e.mu.Unlock()

	fork := &Engine{
		scorer:     e.scorer,
		dag:        newDAG(),
		scores:     newScoreCache(),
		cfg:        e.cfg,
		oracle:     e.oracle,
		edgeFilter: e.edgeFilter,
		logger:     e.logger,
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

func textFromMessage(message *threadv1.Message) string {
	return SerializeMessageForScoring(message)
}
