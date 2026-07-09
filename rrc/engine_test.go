package rrc

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"testing"

	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/proto/pbtext"
	"github.com/elijahmontenegro/grudge/rrc/calibrate"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- Mock implementations ---

// mockScorer returns configurable reranker scores keyed by
// (prior candidate text, query text). The engine's Rerank call passes
// the new-chunk text as query and prior-chunk texts as candidates —
// keying the map as "candidate|query" matches the SetScore(prior, new)
// call convention used below.
type mockScorer struct {
	pairScores map[string]float64
	callCount  int
}

func newMockScorer() *mockScorer {
	return &mockScorer{pairScores: make(map[string]float64)}
}

// SetScore registers the reranker score the mock will return for a
// (prior, new) text pair. `prior` is the candidate text the reranker
// is scoring; `new` is the query text (the new-chunk being scored
// against its priors in OnMessage).
func (m *mockScorer) SetScore(prior, new string, score float64) {
	m.pairScores[prior+"|"+new] = score
}

func (m *mockScorer) Score(_ context.Context, query string, candidates []string) ([]float64, error) {
	m.callCount++
	scores := make([]float64, len(candidates))
	for i, c := range candidates {
		scores[i] = m.pairScores[c+"|"+query]
	}
	return scores, nil
}

// mockChunkOracle treats each message as a single chunk at index 0
// whose text is the message's Content[*].Text concatenation. Vectors
// are a deterministic 1-element slice so cosine prefilter is a no-op
// (all pairs score the same, stable-sort preserves input order).
type mockChunkOracle struct {
	texts     map[string]string
	threads   map[string]string  // id → thread, mirrors the real oracle's RAM map
	retrieval map[string]float64 // text → RetrievalScore for nil-scorer path
}

func newMockChunkOracle() *mockChunkOracle {
	return &mockChunkOracle{
		texts:     make(map[string]string),
		threads:   make(map[string]string),
		retrieval: make(map[string]float64),
	}
}

// Register records a message's text so subsequent ChunksForMessages
// calls return a chunk for it. Test helpers call this implicitly via
// addMsg() — direct callers can use it for edge cases.
func (o *mockChunkOracle) Register(messageID, text string) {
	o.texts[messageID] = text
}

// SetRetrievalScore stamps a RetrievalScore onto chunks whose text
// matches. Used by tests that exercise the nil-scorer engine
// path where RetrievalScore feeds edge formation directly.
func (o *mockChunkOracle) SetRetrievalScore(text string, score float64) {
	o.retrieval[text] = score
}

func (o *mockChunkOracle) ChunksForMessages(_ context.Context, ids []string) (map[string][]ChunkRef, error) {
	out := make(map[string][]ChunkRef)
	for _, id := range ids {
		if t, ok := o.texts[id]; ok && t != "" {
			out[id] = []ChunkRef{{
				MessageID:  id,
				ChunkIndex: 0,
				Text:       t,
				Vector:     nil, // engine calls EnsureVector for new chunks; priors pass through with nil → cosine=0 → stable order
			}}
		}
	}
	return out, nil
}

func (o *mockChunkOracle) EnsureVector(_ context.Context, _ ChunkRef) ([]float32, error) {
	return []float32{1.0}, nil
}

func (o *mockChunkOracle) NearestChunks(_ context.Context, _ string, k int, predicate Predicate) ([]ChunkRef, error) {
	out := make([]ChunkRef, 0, len(o.texts))
	for id, t := range o.texts {
		if t == "" {
			continue
		}
		// Apply the predicate (scope + local-context exclusion) like the real
		// oracle — the engine no longer post-filters against a corpus scan.
		if predicate != nil && !EvalPredicate(predicate, CandidateAttrs{
			MessageID: id,
			ThreadID:  o.threads[id],
			Metadata:  map[string]string{"model_id": "mock"},
		}) {
			continue
		}
		out = append(out, ChunkRef{
			MessageID: id, ChunkIndex: 0, ThreadID: o.threads[id], Text: t,
			RetrievalScore: o.retrieval[t],
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RetrievalScore != out[j].RetrievalScore {
			return out[i].RetrievalScore > out[j].RetrievalScore
		}
		return out[i].MessageID < out[j].MessageID
	})
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// RepresentativeVectors in the mock returns no vectors: the engine's
// ApplyMMR treats an empty representation map as "diversity penalty
// unmeasurable" and leaves the input unchanged — a deterministic
// identity for tests that don't exercise MMR. Tests that do use
// vectorOracle below.
func (o *mockChunkOracle) RepresentativeVectors(_ context.Context, _ []string) (map[string][]float32, error) {
	return nil, nil
}

// --- Helpers ---

// sliceStore is a CorpusStore backed by an in-memory message slice — the test
// analogue of the DB-backed store the bridge provides. TurnPeers groups by
// (thread, turn); test messages share the empty turn, so it returns all
// same-thread messages, a harmless superset for protocol closure.
type sliceStore []*threadv1.Message

func (s sliceStore) Messages(ids []string) (map[string]*threadv1.Message, error) {
	byID := make(map[string]*threadv1.Message, len(s))
	for _, m := range s {
		byID[m.Id] = m
	}
	out := make(map[string]*threadv1.Message, len(ids))
	for _, id := range ids {
		if m, ok := byID[id]; ok {
			out[id] = m
		}
	}
	return out, nil
}

func (s sliceStore) TurnPeers(msgs []*threadv1.Message) ([]*threadv1.Message, error) {
	want := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		want[m.ThreadId+"\x00"+m.TurnId] = true
	}
	var out []*threadv1.Message
	for _, m := range s {
		if want[m.ThreadId+"\x00"+m.TurnId] {
			out = append(out, m)
		}
	}
	return out, nil
}

func makeMsg(id string, position int64, threadID string, text string) *threadv1.Message {
	return &threadv1.Message{
		Id:        id,
		Role:      threadv1.Role_ROLE_USER,
		Content:   []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: text}}}},
		Position:  position,
		ThreadId:  threadID,
		CreatedAt: timestamppb.Now(),
	}
}

// addMsg is a convenience constructor that also registers the text
// with the oracle — every test that feeds messages to OnMessage must
// have the oracle know about them.
func addMsg(o *mockChunkOracle, id string, position int64, threadID string, text string) *threadv1.Message {
	m := makeMsg(id, position, threadID, text)
	o.Register(id, text)
	o.threads[id] = threadID
	return m
}

// testEngine wires a fresh engine with the mock scorer and oracle.
// The fixtures use 0.3/0.5/0.8 score values calibrated to a 0.5
// accept boundary, and the flat-spread gate is disabled
// (MinBatchStdDev=0) so these tests exercise acceptance gating only.
// The spread gate has its own dedicated test suite further down.
// testConfig is DefaultConfig with the deterministic test estimator —
// NewEngine's constructor invariant requires one.
func testConfig() EngineConfig {
	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}
	return cfg
}

func testEngine(mc *mockScorer, o *mockChunkOracle) *Engine {
	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}
	// Fixtures assert accept/reject at a 0.5 similarity boundary, so
	// install a bootstrap calibrator centered at 0.5
	// (Predict(0.5,0)=0.5=LossRatio → the boundary) with the mass term
	// off. steep=40 makes it effectively a hard step so 0.5-vs-0.49
	// tests stay crisp.
	cfg.Calibrator = calibrate.Bootstrap(0.5, 40.0, 0)
	cfg.MinBatchStdDev = 0
	return NewEngine(cfg, mc, WithChunkOracle(o))
}

func testSerializedLocalContext(anchor *threadv1.Message) *SerializedLocalContext {
	return &SerializedLocalContext{
		EventID:     "sel-" + anchor.Id,
		Fingerprint: "test-" + anchor.Id,
		MessageIDs:  []string{anchor.Id},
		Chunks:      []SerializedLocalContextChunk{{Index: 0, Text: strings.TrimSpace(pbtext.TextFromBlocks(anchor.Content))}},
	}
}

// --- Tests ---

func TestNewEngine(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}
	e := NewEngine(cfg, newMockScorer())

	if e.scorer == nil {
		t.Fatal("scorer should not be nil")
	}
	// The acceptance contract lives in the calibrator + LossRatio, not
	// a flat threshold. Document that DefaultConfig ships the
	// precision-first stance.
	if e.cfg.LossRatio != 0.5 {
		t.Fatalf("expected default LossRatio 0.5, got %f", e.cfg.LossRatio)
	}
}

func TestSelectPrereqs_EmptyCorpus(t *testing.T) {
	e := testEngine(newMockScorer(), newMockChunkOracle())
	msg := makeMsg("m1", 0, "t1", "hello")

	edges, _, err := e.selectViaFixture(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 {
		t.Fatalf("empty corpus should return no edges, got %d", len(edges))
	}
}

func TestSelectPrereqs_NilScorer(t *testing.T) {
	// A nil scorer is allowed: the engine takes the Layer-1
	// retrieval score from ChunkRef.RetrievalScore as the candidate's
	// score directly and runs it through calibrated acceptance. The
	// flat-spread gate (MinBatchStdDev) only fires in reranker mode.
	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}
	cfg.MinBatchStdDev = 0 // gate doesn't run, but pin the disable for clarity
	o := newMockChunkOracle()
	e := NewEngine(cfg, nil, WithChunkOracle(o))
	msg := addMsg(o, "m1", 1, "t1", "hello")
	addMsg(o, "m0", 0, "t1", "hi")
	// The mock oracle's NearestChunks needs to surface the prior "hi" with a
	// RetrievalScore above the accept boundary for an edge to form.
	o.SetRetrievalScore("hi", 0.8)

	edges, _, err := e.selectViaFixture(context.Background(), msg)
	if err != nil {
		t.Fatalf("nil scorer should not error, got %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from RetrievalScore, got %d", len(edges))
	}
	if edges[0].Score < 0.5 {
		t.Fatalf("edge score should reflect RetrievalScore, got %v", edges[0].Score)
	}
}

func TestSelectPrereqs_NilOracle(t *testing.T) {
	// Symmetric to nil scorer: no oracle means OnMessage cannot
	// resolve chunks. Same failure class — surface, don't silently
	// produce zero edges.
	e := NewEngine(testConfig(), newMockScorer())
	msg := makeMsg("m1", 1, "t1", "hello")

	_, _, err := e.selectViaFixture(context.Background(), msg)
	if !errors.Is(err, ErrScorerUnavailable) {
		t.Fatalf("expected ErrScorerUnavailable, got %v", err)
	}
}

func TestSelectPrereqs_BelowThreshold_SameThread(t *testing.T) {
	// Edge formation gates on raw CE — below the accept boundary
	// produces no edge regardless of thread relationship.
	mc := newMockScorer()
	mc.SetScore("hi", "hello", 0.3) // reranker 0.3
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	addMsg(o, "m0", 0, "t1", "hi")
	m1 := addMsg(o, "m1", 1, "t1", "hello")

	edges, _, err := e.selectViaFixture(context.Background(), m1)
	if err != nil {
		t.Fatal(err)
	}
	// CE=0.3 < 0.5 accept boundary → no edge.
	if len(edges) != 0 {
		t.Fatalf("below-threshold CE should produce no edge, got %d", len(edges))
	}
}

func TestSelectPrereqs_BelowThreshold_CrossThread(t *testing.T) {
	mc := newMockScorer()
	mc.SetScore("hi", "hello", 0.3)
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	addMsg(o, "m0", 0, "t-other", "hi")
	m1 := addMsg(o, "m1", 1, "t1", "hello")

	edges, _, err := e.selectViaFixture(context.Background(), m1)
	if err != nil {
		t.Fatal(err)
	}
	// Cross-thread or same-thread, the gate is the same: CE 0.3 < 0.5.
	if len(edges) != 0 {
		t.Fatalf("cross-thread below-threshold pair should create no edge, got %d", len(edges))
	}
}

func TestSelectPrereqs_AboveThreshold(t *testing.T) {
	mc := newMockScorer()
	mc.SetScore("what is a tomato cake", "tell me more about tomato cake", 0.8)
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	addMsg(o, "m0", 0, "t1", "what is a tomato cake")
	m1 := addMsg(o, "m1", 1, "t1", "tell me more about tomato cake")

	edges, _, err := e.selectViaFixture(context.Background(), m1)
	if err != nil {
		t.Fatal(err)
	}
	// CE=0.8 ≥ 0.5 accept boundary → edge.
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}

	edge := edges[0]
	if edge.FromMessageId != "m0" || edge.ToMessageId != "m1" {
		t.Fatalf("wrong edge endpoints: %s -> %s", edge.FromMessageId, edge.ToMessageId)
	}
	if edge.CrossEncoderScore != 0.8 {
		t.Fatalf("expected CE score 0.8, got %f", edge.CrossEncoderScore)
	}
	if edge.Source != rrcv1.EdgeSource_EDGE_SOURCE_CROSS_ENCODER {
		t.Fatalf("expected CE source, got %v", edge.Source)
	}
}

func TestSelectPrereqs_MultipleCorpusMessages(t *testing.T) {
	mc := newMockScorer()
	mc.SetScore("hello", "how are you", 0.7)
	mc.SetScore("nice weather", "how are you", 0.2)
	mc.SetScore("tell me a joke", "how are you", 0.6)
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	addMsg(o, "m0", 0, "t1", "hello")
	addMsg(o, "m1", 1, "t1", "nice weather")
	addMsg(o, "m2", 2, "t1", "tell me a joke")
	prompt := addMsg(o, "m3", 3, "t1", "how are you")

	edges, _, err := e.selectViaFixture(context.Background(), prompt)
	if err != nil {
		t.Fatal(err)
	}
	// CE-only gating: 0.7 ≥ 0.5 (m0), 0.2 < 0.5 (m1), 0.6 ≥ 0.5 (m2).
	// With adaptive gates disabled by testEngine, just the absolute
	// threshold applies: 2 edges.
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges above threshold, got %d", len(edges))
	}
}

func TestSelect_NoEdges(t *testing.T) {
	e := testEngine(newMockScorer(), newMockChunkOracle())

	result, err := e.Select("m0", threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) != 0 {
		t.Fatalf("expected empty selection for message with no edges, got %d", len(result.Selected))
	}
}

func TestSelect_LinearChain(t *testing.T) {
	mc := newMockScorer()
	// CE-only gating. Above-threshold (≥0.5) chain: a→b→c→d. Below-
	// threshold off-chain links should not form edges.
	mc.SetScore("a", "b", 0.8)
	mc.SetScore("b", "c", 0.7)
	mc.SetScore("c", "d", 0.9)
	mc.SetScore("a", "c", 0.3)
	mc.SetScore("a", "d", 0.2)
	mc.SetScore("b", "d", 0.3)
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	ctx := context.Background()
	msgs := []*threadv1.Message{
		addMsg(o, "m0", 0, "t1", "a"),
		addMsg(o, "m1", 1, "t1", "b"),
		addMsg(o, "m2", 2, "t1", "c"),
		addMsg(o, "m3", 3, "t1", "d"),
	}

	if _, _, err := e.selectViaFixture(ctx, msgs[1]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.selectViaFixture(ctx, msgs[2]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.selectViaFixture(ctx, msgs[3]); err != nil {
		t.Fatal(err)
	}

	result, err := e.Select("m3", threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) == 0 {
		t.Fatal("expected at least one selected message")
	}

	found := false
	for _, s := range result.Selected {
		if s.MessageId == "m2" {
			found = true
			if s.HopDepth != 1 {
				t.Fatalf("m2 should be hop depth 1, got %d", s.HopDepth)
			}
		}
	}
	if !found {
		t.Fatal("m2 should be selected as direct prerequisite of m3")
	}
}

func TestSelect_ProbabilityFloorCutoff(t *testing.T) {
	// DAG-direct edge insert; independent of OnMessage path.
	e := testEngine(newMockScorer(), newMockChunkOracle())

	// Edge probability below the LossRatio floor (0.5).
	e.dag.AddEdge(&rrcv1.Edge{
		FromMessageId: "m0", ToMessageId: "m1",
		Score:        0.1,
		FromThreadId: "t1", ToThreadId: "t1",
	})

	result, err := e.Select("m1", threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) != 0 {
		t.Fatalf("scores below floor should be excluded, got %d selected", len(result.Selected))
	}
}

func TestSelect_ThreadScope(t *testing.T) {
	e := testEngine(newMockScorer(), newMockChunkOracle())

	// Both edges are SEEDS (hop-1, into the anchor): the walk gates them
	// on their stored verdicts — this event's own Score (0.8/0.9 clear
	// the 0.5 floor). CrossEncoderScore is the recorded observation that
	// hop≥2 traversal would derive from (relationalEdgeScore).
	e.dag.AddEdge(&rrcv1.Edge{
		FromMessageId: "m0", ToMessageId: "m2",
		Score: 0.8, CrossEncoderScore: 1.0,
		FromThreadId: "t1", ToThreadId: "t1",
	})
	e.dag.AddEdge(&rrcv1.Edge{
		FromMessageId: "m1", ToMessageId: "m2",
		Score: 0.9, CrossEncoderScore: 1.0,
		FromThreadId: "t2", ToThreadId: "t1",
	})

	result, err := e.Select("m2", threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) != 1 {
		t.Fatalf("thread scope should select 1 (not cross-thread), got %d", len(result.Selected))
	}
	if result.Selected[0].MessageId != "m0" {
		t.Fatalf("expected m0 selected, got %s", result.Selected[0].MessageId)
	}

	result, err = e.Select("m2", threadv1.SelectionScope_SELECTION_SCOPE_ALL_THREADS, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) != 2 {
		t.Fatalf("all-threads scope should select 2, got %d", len(result.Selected))
	}
}

func TestFork(t *testing.T) {
	mc := newMockScorer()
	mc.SetScore("hello", "world", 0.8)
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	addMsg(o, "m0", 0, "t1", "hello")
	m1 := addMsg(o, "m1", 1, "t1", "world")
	if _, _, err := e.selectViaFixture(context.Background(), m1); err != nil {
		t.Fatal(err)
	}

	fork := e.Fork()

	parentEdges := e.dag.AllEdges()
	forkEdges := fork.dag.AllEdges()
	if len(forkEdges) != len(parentEdges) {
		t.Fatalf("fork should have %d edges, got %d", len(parentEdges), len(forkEdges))
	}

	if fork.scorer != e.scorer {
		t.Fatal("fork should share scorer reference")
	}
}

func TestMerge(t *testing.T) {
	mc := newMockScorer()
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	e.dag.AddEdge(&rrcv1.Edge{
		FromMessageId: "m0", ToMessageId: "m1",
		Score: 0.8, FromThreadId: "t1", ToThreadId: "t1",
	})

	fork := e.Fork()

	fork.dag.AddEdge(&rrcv1.Edge{
		FromMessageId: "m1", ToMessageId: "m2",
		Score: 0.7, FromThreadId: "t1", ToThreadId: "t1",
	})

	if err := e.Merge(fork); err != nil {
		t.Fatal(err)
	}

	allEdges := e.dag.AllEdges()
	if len(allEdges) < 2 {
		t.Fatalf("after merge, parent should have at least 2 edges, got %d", len(allEdges))
	}
	found := false
	for _, edge := range allEdges {
		if edge.FromMessageId == "m1" && edge.ToMessageId == "m2" {
			found = true
		}
	}
	if !found {
		t.Fatal("fork's new edge should be in parent after merge")
	}
}

func TestDAG_DualIndex(t *testing.T) {
	dag := newDAG()
	edge := &rrcv1.Edge{
		FromMessageId: "a", ToMessageId: "b",
		Score: 0.8, FromThreadId: "t1", ToThreadId: "t1",
	}
	dag.AddEdge(edge)

	prereqs := dag.Prerequisites("b")
	if len(prereqs) != 1 || prereqs[0].FromMessageId != "a" {
		t.Fatal("Prerequisites should return edges pointing into the message")
	}

	deps := dag.Dependents("a")
	if len(deps) != 1 || deps[0].ToMessageId != "b" {
		t.Fatal("Dependents should return edges pointing out of the message")
	}

	if !dag.HasMessage("a") || !dag.HasMessage("b") {
		t.Fatal("HasMessage should return true for both endpoints")
	}
	if dag.HasMessage("c") {
		t.Fatal("HasMessage should return false for unknown message")
	}
}

func TestScoreCache(t *testing.T) {
	sc := newScoreCache()

	// Score cache is keyed by exact Local Context serialization and candidate chunk.
	sc.setLocalContext("context-a", 0, "candidate-b", 0, 0.75)

	score, ok := sc.getLocalContext("context-a", 0, "candidate-b", 0)
	if !ok || score != 0.75 {
		t.Fatalf("expected 0.75, got %f (ok=%v)", score, ok)
	}

	if _, ok := sc.getLocalContext("context-b", 0, "candidate-b", 0); ok {
		t.Fatal("different Local Context fingerprint should not be cached")
	}

	if _, ok := sc.getLocalContext("context-a", 0, "candidate-x", 0); ok {
		t.Fatal("unknown candidate should return false")
	}

	if _, ok := sc.getLocalContext("context-a", 1, "candidate-b", 0); ok {
		t.Fatal("different Local Context chunk should not be cached")
	}
}

func TestSelect_TransitiveReduction(t *testing.T) {
	e := testEngine(newMockScorer(), newMockChunkOracle())

	// Diamond: m0 -> m2, m0 -> m1 -> m2. Direct m0->m2 is redundant.
	// CrossEncoderScore set directly because extractSubgraph
	// re-projects under current config.
	e.dag.AddEdge(&rrcv1.Edge{
		FromMessageId: "m0", ToMessageId: "m2",
		Score: 0.6, CrossEncoderScore: 1.0,
		FromThreadId: "t1", ToThreadId: "t1",
	})
	e.dag.AddEdge(&rrcv1.Edge{
		FromMessageId: "m1", ToMessageId: "m2",
		Score: 0.8, CrossEncoderScore: 1.0,
		FromThreadId: "t1", ToThreadId: "t1",
	})
	e.dag.AddEdge(&rrcv1.Edge{
		FromMessageId: "m0", ToMessageId: "m1",
		Score: 0.7, CrossEncoderScore: 1.0,
		FromThreadId: "t1", ToThreadId: "t1",
	})

	result, err := e.Select("m2", threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Selected) != 2 {
		t.Fatalf("expected 2 selected, got %d", len(result.Selected))
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}
	if cfg.LossRatio != 0.5 {
		t.Fatalf("expected LossRatio 0.5 (precision-first stance), got %f", cfg.LossRatio)
	}
	if cfg.MinBatchStdDev != 0.05 {
		t.Fatalf("expected MinBatchStdDev 0.05 (flat-spread guard), got %f", cfg.MinBatchStdDev)
	}
	// The bootstrap calibrator centers the accept boundary near the
	// old 0.60 operating point: Predict(0.60, 0) ≈ LossRatio.
	p := cfg.Calibrator.Predict(0.60, 0)
	if p < 0.45 || p > 0.55 {
		t.Fatalf("bootstrap calibrator should put sim=0.60/mass=0 at the accept boundary, got P=%f", p)
	}
}

func TestSelectPrereqs_ScoreCachePopulated(t *testing.T) {
	mc := newMockScorer()
	mc.SetScore("a", "b", 0.3) // reranker 0.3 — below the accept boundary
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	addMsg(o, "m0", 0, "t1", "a")
	m1 := addMsg(o, "m1", 1, "t1", "b")

	if _, _, err := e.selectViaFixture(context.Background(), m1); err != nil {
		t.Fatal(err)
	}

	// Score cache keyed at chunk granularity (both messages have one
	// chunk at index 0). The reranker score is cached even when no
	// edge was emitted — future OnMessage calls touching this pair
	// skip the reranker entirely.
	score, ok := e.scores.getLocalContext("fixture-m1", 0, "m0", 0)
	if !ok {
		t.Fatal("score should be cached")
	}
	if diff := score - 0.3; diff > 0.01 || diff < -0.01 {
		t.Fatalf("cached score should be ~0.3, got %f", score)
	}
}

func TestSelectPrereqs_SkipsSelf(t *testing.T) {
	mc := newMockScorer()
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	m0 := addMsg(o, "m0", 0, "t1", "hello")
	// The anchor is the only registered message — it must be filtered out as
	// self, and no scorer call should happen.
	edges, _, err := e.selectViaFixture(context.Background(), m0)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 {
		t.Fatal("should not create edges to self")
	}
	if mc.callCount != 0 {
		t.Fatal("should not call scorer when only self in corpus")
	}
}

// --- Gate discrimination tests ---
//
// Gates stack:
//   Gate 1 (acceptance): calibrated P(prereq|sim,mass) < LossRatio → skip
//   Gate 3 (batch):      batchStddev < MinBatchStdDev → zero edges for the whole batch
//
// (Gate 2, the z-score relative-standout test, was subsumed by
// calibration and removed in A4.)
//
// Batch stats are computed over candidates that actually got rescored
// (either fresh Rerank call or cached score), not over priors that
// fell outside RerankTopK with no cache hit. That invariant has its
// own dedicated test below — without it, zero-score synthetic entries
// would distort mean/stddev and fire the gates against the wrong
// baseline.

// threeGateConfig enables the adaptive gates that testEngine disables.
// Edge-gating substrate is raw CE — the mock CE values drive the
// distribution directly.
func threeGateConfig() EngineConfig {
	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}
	// Bootstrap calibrator centered at 0.5 with the mass term off, so the
	// legacy fixtures' 0.1/0.4/0.5/0.6 CE values map to accept/reject at the
	// same boundary through A4's calibrated path (Predict(0.5,0)=0.5). steep
	// makes it a near-hard step for crisp boundary assertions. MinBatchStdDev
	// (Gate 3) is retained — it's orthogonal to acceptance and survived A4.
	// The z-score gate (Gate 2) was subsumed by calibration and removed;
	// Gate-2-specific tests are updated to the calibrated model.
	cfg.Calibrator = calibrate.Bootstrap(0.5, 40.0, 0)
	cfg.MinBatchStdDev = 0.05
	return cfg
}

func threeGateEngine(mc *mockScorer, o *mockChunkOracle) *Engine {
	return NewEngine(threeGateConfig(), mc, WithChunkOracle(o))
}

func TestSelectPrereqs_Gate1_AbsoluteThreshold(t *testing.T) {
	// CE-only gating: one clears the 0.5 accept boundary, one doesn't.
	// Note CE=0.1 rather than 0.0: the engine's aggregation uses a
	// strict `>` against zero-init, so CE=0.0 is treated as unscored
	// and the candidate never enters the batch. Any non-zero CE
	// below the boundary exercises gate 1 correctly.
	mc := newMockScorer()
	mc.SetScore("low", "query", 0.1)
	mc.SetScore("high", "query", 0.5)
	o := newMockChunkOracle()
	e := threeGateEngine(mc, o)

	addMsg(o, "m0", 0, "tA", "low")
	addMsg(o, "m1", 0, "tB", "high")
	q := addMsg(o, "q", 0, "tQ", "query")

	edges, _, err := e.selectViaFixture(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	// Batch stats over [0.1, 0.5]: mean=0.3, stddev=0.2.
	// Gate 3: 0.2 > 0.05 — no fire.
	// Gate 1: 0.1 < 0.5 fails, 0.5 not<0.5 passes.
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (low gated by gate 1), got %d", len(edges))
	}
	if edges[0].FromMessageId != "m1" {
		t.Fatalf("wrong prior emitted an edge: %s", edges[0].FromMessageId)
	}
}

func TestSelectPrereqs_CrossThreadGatesUniformly(t *testing.T) {
	// After the WeightTemp removal, edge formation is a single CE
	// gate that applies uniformly to same-thread and cross-thread
	// candidates — no asymmetric distribution, no second threshold.
	// CE=0.6 clears the default accept boundary (bootstrap centered at
	// 0.60) from either thread relationship.
	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}
	cfg.MinBatchStdDev = 0

	mc := newMockScorer()
	mc.SetScore("cross", "q", 0.6)
	mc.SetScore("same", "q", 0.6)
	o := newMockChunkOracle()
	o.SetRetrievalScore("a", 0.9)
	o.SetRetrievalScore("b", 0.8)
	o.SetRetrievalScore("c", 0.7)
	o.SetRetrievalScore("d", 0.6)
	o.SetRetrievalScore("e", 0.5)
	e := NewEngine(cfg, mc, WithChunkOracle(o))

	addMsg(o, "mCross", 0, "tOther", "cross")
	addMsg(o, "mSame", 0, "tQ", "same")
	q := addMsg(o, "q", 1, "tQ", "q")

	edges, _, err := e.selectViaFixture(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 2 {
		t.Fatalf("CE=0.6 should clear acceptance for both thread relationships, got %d edges", len(edges))
	}
	// Sub-threshold CE rejected uniformly too.
	mc2 := newMockScorer()
	mc2.SetScore("low-cross", "q2", 0.4)
	mc2.SetScore("low-same", "q2", 0.4)
	o2 := newMockChunkOracle()
	e2 := NewEngine(cfg, mc2, WithChunkOracle(o2))
	addMsg(o2, "lowCross", 0, "tOther", "low-cross")
	addMsg(o2, "lowSame", 0, "tQ", "low-same")
	q2 := addMsg(o2, "q2", 1, "tQ", "q2")
	rejected, _, err := e2.selectViaFixture(context.Background(), q2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 0 {
		t.Fatalf("CE=0.4 should be gated by calibrated acceptance, got %d edges", len(rejected))
	}
}

func TestSelectPrereqs_ZScoreGateRemoved_ClusterAllAccepted(t *testing.T) {
	// A4 removed the z-score "relative standout" gate: it was a statistical
	// patch for a flat threshold's brittleness, and calibrated probability
	// subsumes that job. So a cluster of candidates that all clear the
	// calibrated acceptance floor now ALL form edges — the relative-standout
	// suppression is gone by design. (Before A4 this kept only the 0.70
	// outlier; the assertion is inverted to document the new contract.)
	//   CE=0.55 (x2, cluster) + CE=0.70 (outlier) — all clear the 0.5 floor.
	mc := newMockScorer()
	mc.SetScore("a", "q", 0.55)
	mc.SetScore("b", "q", 0.55)
	mc.SetScore("c", "q", 0.70)
	o := newMockChunkOracle()
	e := threeGateEngine(mc, o)

	addMsg(o, "m0", 0, "tQ", "a")
	addMsg(o, "m1", 0, "tQ", "b")
	addMsg(o, "m2", 0, "tQ", "c")
	q := addMsg(o, "q", 0, "tQ", "q")

	edges, _, err := e.selectViaFixture(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 3 {
		t.Fatalf("z-gate removed: all 3 cluster members clearing the floor should form edges, got %d", len(edges))
	}
}

func TestSelectPrereqs_Gate3_BatchIndiscriminate(t *testing.T) {
	// Tight cluster of above-threshold candidates — stddev falls
	// below MinBatchStdDev. Gate 3 fires for the whole batch: zero
	// edges even though every candidate clears gate 1.
	mc := newMockScorer()
	mc.SetScore("a", "q", 0.51)
	mc.SetScore("b", "q", 0.52)
	mc.SetScore("c", "q", 0.53)
	o := newMockChunkOracle()
	e := threeGateEngine(mc, o)

	addMsg(o, "m0", 0, "tQ", "a")
	addMsg(o, "m1", 0, "tQ", "b")
	addMsg(o, "m2", 0, "tQ", "c")
	q := addMsg(o, "q", 0, "tQ", "q")

	edges, _, err := e.selectViaFixture(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	// Batch [0.51, 0.52, 0.53]: stddev ≈ 0.00816 < MinBatchStdDev=0.05.
	// Gate 3 short-circuits → no edges (even though all three clear
	// the 0.5 accept boundary).
	if len(edges) != 0 {
		t.Fatalf("tight cluster must return zero edges via gate 3, got %d", len(edges))
	}
}

func TestSelectPrereqs_Gate3_Disabled(t *testing.T) {
	// MinBatchStdDev=0 disables gate 3 (the batch-flatness kill, which
	// survived A4 — it's orthogonal to acceptance). With gate 3 off, a tight
	// cluster that all clears the calibrated floor forms edges for every
	// member — there is no longer a z-score gate to keep only the top. (Pre-
	// A4 this expected 1; post-A4 the z-gate is gone, so all 3 form edges.)
	cfg := threeGateConfig()
	cfg.MinBatchStdDev = 0
	mc := newMockScorer()
	mc.SetScore("a", "q", 0.51)
	mc.SetScore("b", "q", 0.52)
	mc.SetScore("c", "q", 0.53)
	o := newMockChunkOracle()
	e := NewEngine(cfg, mc, WithChunkOracle(o))

	addMsg(o, "m0", 0, "tQ", "a")
	addMsg(o, "m1", 0, "tQ", "b")
	addMsg(o, "m2", 0, "tQ", "c")
	q := addMsg(o, "q", 0, "tQ", "q")

	edges, _, err := e.selectViaFixture(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 3 {
		t.Fatalf("gate 3 off + z-gate removed: all 3 clearing the floor should form edges, got %d", len(edges))
	}
}

func TestSelectPrereqs_SingleCandidateAccepted(t *testing.T) {
	// Single-candidate batch has stddev=0. Gate 3 would normally fire
	// on stddev=0, but MinBatchStdDev=0 in this test to isolate the
	// single-candidate acceptance case.
	cfg := threeGateConfig()
	cfg.MinBatchStdDev = 0
	mc := newMockScorer()
	mc.SetScore("a", "q", 0.5) // CE=0.5 sits exactly at the accept boundary → passes
	o := newMockChunkOracle()
	e := NewEngine(cfg, mc, WithChunkOracle(o))

	addMsg(o, "m0", 0, "tA", "a")
	q := addMsg(o, "q", 0, "tQ", "q")

	edges, _, err := e.selectViaFixture(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("single candidate clearing acceptance should form an edge, got %d edges", len(edges))
	}
}

func TestSelectPrereqs_RescoredFilterInvariant(t *testing.T) {
	// Batch stats must be computed over rescored priors only. Priors
	// beyond RerankTopK with no cached score contribute no CE and
	// must be excluded from mean/stddev — otherwise synthetic zeros
	// depress the mean and inflate stddev, and the gates fire
	// against the wrong baseline.
	//
	// Setup: 5 priors with CE values such that only the top 2 are
	// scored under RerankTopK=2. Tight CE cluster (0.78/0.72) has
	// stddev=0.03 — below MinBatchStdDev=0.05 — so gate 3 fires
	// when stats are computed over the actual two-element batch.
	// If priors beyond top-K were synthesized as zeros, stddev
	// would explode and gate 3 wouldn't fire.
	cfg := threeGateConfig()
	cfg.RerankTopK = 2

	mc := newMockScorer()
	mc.SetScore("a", "q", 0.78)
	mc.SetScore("b", "q", 0.72)
	mc.SetScore("c", "q", 0.6) // not scored under RerankTopK=2
	mc.SetScore("d", "q", 0.5) // not scored
	mc.SetScore("e", "q", 0.4) // not scored

	o := newMockChunkOracle()
	e := NewEngine(cfg, mc, WithChunkOracle(o))

	addMsg(o, "m0", 0, "t1", "a")
	addMsg(o, "m1", 1, "t1", "b")
	addMsg(o, "m2", 2, "t1", "c")
	addMsg(o, "m3", 3, "t1", "d")
	addMsg(o, "m4", 4, "t1", "e")
	q := addMsg(o, "q", 5, "t1", "q")

	edges, _, err := e.selectViaFixture(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	// Rescored-filter invariant holds → gate 3 fires on the narrow
	// two-element batch → zero edges.
	if len(edges) != 0 {
		t.Fatalf("rescored-filter invariant broken: expected 0 edges (gate 3 fires on narrow batch), got %d", len(edges))
	}
}

func TestSelectPrereqs_CachedScoresCountAsRescored(t *testing.T) {
	// A prior with a cached score counts as "rescored" for batch
	// aggregation — no fresh Rerank call needed, but it still enters
	// candidates and batch stats. Verifies the aggregation path
	// (cached OR in rerankSet) per engine.go lines 287-297.
	cfg := threeGateConfig()
	cfg.MinBatchStdDev = 0 // isolate the aggregation path

	mc := newMockScorer()
	mc.SetScore("a", "q", 0.8)
	mc.SetScore("b", "q", 0.5)

	o := newMockChunkOracle()
	e := NewEngine(cfg, mc, WithChunkOracle(o))

	addMsg(o, "m0", 0, "t1", "a")
	addMsg(o, "m1", 1, "t1", "b")
	q := addMsg(o, "q", 2, "t1", "q")

	// Pre-seed the score cache for the m0→q pair. The engine should
	// see the cached value instead of calling Rerank on this pair.
	e.scores.setLocalContext("fixture-q", 0, "m0", 0, 0.8)

	_, _, err := e.selectViaFixture(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	// m0 via cache, m1 via fresh rerank — both enter candidates and
	// batch stats. Exactly one fresh Rerank call for m1.
	if mc.callCount != 1 {
		t.Fatalf("cached m0 should skip rerank; expected 1 call (for m1 only), got %d", mc.callCount)
	}
}

// --- MMR (Phase B.1) ---

// vectorOracle is a ChunkOracle that returns a fixed vector per
// message ID. Supports MMR tests: cosine similarity between candidates
// is the distance measure MMR actually penalizes against.
type vectorOracle struct {
	vectors map[string][]float32
}

func newVectorOracle() *vectorOracle {
	return &vectorOracle{vectors: make(map[string][]float32)}
}

func (o *vectorOracle) set(id string, v []float32) { o.vectors[id] = v }

func (o *vectorOracle) ChunksForMessages(_ context.Context, ids []string) (map[string][]ChunkRef, error) {
	out := make(map[string][]ChunkRef)
	for _, id := range ids {
		if v, ok := o.vectors[id]; ok {
			out[id] = []ChunkRef{{MessageID: id, ChunkIndex: 0, Text: id, Vector: v}}
		}
	}
	return out, nil
}

func (o *vectorOracle) NearestChunks(_ context.Context, _ string, k int, _ Predicate) ([]ChunkRef, error) {
	out := make([]ChunkRef, 0, len(o.vectors))
	for id, v := range o.vectors {
		out = append(out, ChunkRef{MessageID: id, ChunkIndex: 0, Text: id, Vector: v})
	}
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out, nil
}

func (o *vectorOracle) EnsureVector(_ context.Context, ref ChunkRef) ([]float32, error) {
	if v, ok := o.vectors[ref.MessageID]; ok {
		return v, nil
	}
	return nil, nil
}

// RepresentativeVectors: the vectorOracle's stored vectors are the
// representatives directly — the minimal backend seam the engine's
// MMR loop consumes.
func (o *vectorOracle) RepresentativeVectors(_ context.Context, ids []string) (map[string][]float32, error) {
	out := make(map[string][]float32, len(ids))
	for _, id := range ids {
		if v, ok := o.vectors[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

// TestApplyMMR_ReordersNearDuplicates verifies the load-bearing
// assertion of the Phase B.1 design: given a near-duplicate chain of
// high-scoring candidates (the observed "nine copies of let me check
// the chapter" pattern) and one distinct lower-scoring candidate,
// MMR penalizes the duplicates enough that the distinct one emerges
// above most of them in the reordered output.
func TestApplyMMR_ReordersNearDuplicates(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}

	o := newVectorOracle()
	// dup1..dup3 are near-identical vectors (cosine ≈ 1).
	// distinct is orthogonal.
	o.set("dup1", []float32{1, 0, 0})
	o.set("dup2", []float32{0.99, 0.01, 0})
	o.set("dup3", []float32{0.98, 0.02, 0})
	o.set("distinct", []float32{0, 1, 0})
	e := NewEngine(cfg, newMockScorer(), WithChunkOracle(o))

	selected := []*rrcv1.SelectedMessage{
		{MessageId: "dup1", EffectiveScore: 0.95},
		{MessageId: "dup2", EffectiveScore: 0.94},
		{MessageId: "dup3", EffectiveScore: 0.93},
		{MessageId: "distinct", EffectiveScore: 0.60},
	}

	out, err := e.ApplyMMR(context.Background(), selected)
	if err != nil {
		t.Fatalf("ApplyMMR error: %v", err)
	}
	if len(out) != len(selected) {
		t.Fatalf("ApplyMMR must not drop items; got %d want %d", len(out), len(selected))
	}
	// First pick is the highest-orig candidate — dup1.
	if out[0].MessageId != "dup1" {
		t.Errorf("first pick should be highest orig score (dup1); got %s", out[0].MessageId)
	}
	// Second pick is distinct: its novel fraction is 1 (orthogonal), so
	// it keeps full value 0.60, while the dups collapse to
	// 0.94·(1−~0.99) ≈ 0.005.
	if out[1].MessageId != "distinct" {
		t.Errorf("second pick should be distinct (novelty beats the orig-score gap); got %s", out[1].MessageId)
	}
	// Duplicates carry their DISCOUNTED value (≈0, well below original);
	// a fully-novel candidate keeps its original value undiscounted —
	// the novel-fraction form never penalizes what adds new information,
	// and never goes negative (the old λ rewrite provably did).
	for i := 1; i < len(out); i++ {
		got := float64(out[i].EffectiveScore)
		if got < 0 {
			t.Errorf("novel-fraction value must be ≥ 0, got %.3f for %s", got, out[i].MessageId)
		}
		switch out[i].MessageId {
		case "dup2", "dup3":
			if got > 0.1 {
				t.Errorf("near-duplicate %s should be discounted to ≈0, got %.3f", out[i].MessageId, got)
			}
		case "distinct":
			if got < 0.599 {
				t.Errorf("fully-novel candidate keeps its value, got %.3f", got)
			}
		}
	}
}

// TestApplyMMR_NoOracleError verifies ApplyMMR fails loud rather than
// silently pass-through when the oracle isn't wired — an oracle-less
// engine running MMR is a configuration bug, not an acceptable
// degradation mode.
func TestApplyMMR_NoOracleError(t *testing.T) {
	e := NewEngine(testConfig(), newMockScorer())
	// No SetChunkOracle call.
	_, err := e.ApplyMMR(context.Background(), []*rrcv1.SelectedMessage{
		{MessageId: "a", EffectiveScore: 0.5},
		{MessageId: "b", EffectiveScore: 0.4},
	})
	if err == nil {
		t.Fatal("expected ApplyMMR error without oracle wired")
	}
}

// TestApplyMMR_ExactScores pins the MMR arithmetic on the engine-owned
// greedy loop (the oracle contributes only representative vectors).
//
// Setup: three candidates. orig[anchor]=0.95 (highest, becomes the
// MMR anchor). vec(near)==vec(anchor) (cosine 1.0, maximum penalty).
// vec(far) orthogonal to anchor (cosine 0.0, no penalty). Under
// λ=0.5:
//
//	effective(near) = 0.5*orig(near) - 0.5*1.0 = 0.5*0.90 - 0.5 = -0.05
//	effective(far)  = 0.5*orig(far)  - 0.5*0.0 = 0.5*0.50 - 0.0 =  0.25
//
// far beats near despite the original-score gap (0.50 < 0.90).
// Expected ordering: [anchor, far, near].
func TestApplyMMR_ExactScores(t *testing.T) {
	o := newVectorOracle()
	o.set("anchor", []float32{1, 0, 0})
	o.set("near", []float32{1, 0, 0})
	o.set("far", []float32{0, 1, 0})

	selected := []*rrcv1.SelectedMessage{
		{MessageId: "anchor", EffectiveScore: 0.95},
		{MessageId: "near", EffectiveScore: 0.90},
		{MessageId: "far", EffectiveScore: 0.50},
	}
	e := NewEngine(testConfig(), newMockScorer(), WithChunkOracle(o))
	out, err := e.ApplyMMR(context.Background(), selected)
	if err != nil {
		t.Fatalf("ApplyMMR error: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(out))
	}
	want := []string{"anchor", "far", "near"}
	for i, w := range want {
		if out[i].MessageId != w {
			t.Errorf("position %d: want %s, got %s", i, w, out[i].MessageId)
		}
	}

	// Novel-fraction values: near duplicates anchor exactly (cos 1) →
	// 0.90·(1−1) = 0; far is orthogonal → keeps its full 0.50.
	for _, s := range out[1:] {
		switch s.MessageId {
		case "near":
			if math.Abs(float64(s.EffectiveScore)) > 1e-5 {
				t.Errorf("near effective: want 0 (zero novel fraction), got %v", s.EffectiveScore)
			}
		case "far":
			if math.Abs(float64(s.EffectiveScore)-0.50) > 1e-5 {
				t.Errorf("far effective: want 0.50 (fully novel), got %v", s.EffectiveScore)
			}
		}
	}
}

// TestSelect_LiftNeverLaundersIntoTransitivePull pins A3, both
// directions: hop≥2 traversal consumes the recorded OBSERVATION under
// the current calibrator, never the stored verdict. A mass-lifted edge
// (weak dependency 0.38 accepted at P .93 via circumstance) must NOT
// pull transitively on its old verdict; an edge whose old verdict was
// weak but whose observed dependency is strong MUST pull. Inferences
// are perishable; observations keep.
func TestSelect_LiftNeverLaundersIntoTransitivePull(t *testing.T) {
	e := testEngine(newMockScorer(), newMockChunkOracle())

	// Fresh seed into the current anchor: this event's own verdict.
	e.dag.AddEdge(&rrcv1.Edge{
		FromMessageId: "c1", ToMessageId: "anchor",
		Score: 0.9, CrossEncoderScore: 0.8,
		FromThreadId: "t1", ToThreadId: "t1",
	})
	// Fossil A: past turn accepted c2 into c1's generation via MASS
	// (observed dependency 0.38, stored verdict 0.93). The lift was that
	// turn's circumstance — it must not survive as dependency strength.
	e.dag.AddEdge(&rrcv1.Edge{
		FromMessageId: "c2", ToMessageId: "c1",
		Score: 0.93, CrossEncoderScore: 0.38,
		FromThreadId: "t1", ToThreadId: "t1",
	})
	// Fossil B: strong observed dependency (0.85) whose stored verdict is
	// weak (0.4 — e.g. a stiffer old curve). The observation must carry.
	e.dag.AddEdge(&rrcv1.Edge{
		FromMessageId: "c3", ToMessageId: "c1",
		Score: 0.4, CrossEncoderScore: 0.85,
		FromThreadId: "t1", ToThreadId: "t1",
	})

	result, err := e.Select("anchor", threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]bool{}
	for _, s := range result.Selected {
		selected[s.MessageId] = true
	}
	if !selected["c1"] {
		t.Fatal("seed c1 must be selected (this event's verdict)")
	}
	if selected["c2"] {
		t.Fatal("mass-lifted fossil pulled transitively on its stored verdict — the lift laundered into the walk")
	}
	if !selected["c3"] {
		t.Fatal("strong observed dependency must pull regardless of its weak stored verdict")
	}
}

// TestSelect_RefitReGatesHistoryWithoutRewrite pins A3's retroactivity:
// a calibrator change re-prices every historical hop≥2 edge at walk
// time — no rewrite, no migration, no version stamp. The identical
// stored edge is pulled under a permissive curve and refused under a
// stiff one.
func TestSelect_RefitReGatesHistoryWithoutRewrite(t *testing.T) {
	edges := []*rrcv1.Edge{
		{FromMessageId: "c1", ToMessageId: "anchor",
			Score: 0.9, CrossEncoderScore: 0.8,
			FromThreadId: "t1", ToThreadId: "t1"},
		{FromMessageId: "c2", ToMessageId: "c1",
			Score: 0.9, CrossEncoderScore: 0.65,
			FromThreadId: "t1", ToThreadId: "t1"},
	}
	run := func(cfg EngineConfig) map[string]bool {
		e := NewEngine(cfg, newMockScorer(), WithChunkOracle(newMockChunkOracle()))
		for _, ed := range edges {
			e.dag.AddEdge(ed)
		}
		result, err := e.Select("anchor", threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, s := range result.Selected {
			got[s.MessageId] = true
		}
		return got
	}

	permissive := testConfig()
	permissive.Calibrator = calibrate.Bootstrap(0.50, 12.0, 6.0) // floor 0.50: rel(0.65)≈0.86
	stiff := testConfig()
	stiff.Calibrator = calibrate.Bootstrap(0.75, 12.0, 6.0) // floor 0.75: rel(0.65)≈0.23

	if got := run(permissive); !got["c2"] {
		t.Fatalf("permissive curve must pull the 0.65-dependency chain, got %v", got)
	}
	if got := run(stiff); got["c2"] {
		t.Fatalf("stiff curve must refuse the same stored edge — history re-gated with no rewrite, got %v", got)
	}
}

// TestSelect_ProvenanceWeightIsRawEvidence pins A2-W6: every selected
// entry carries provenance_weight = the product of RAW CrossEncoderScore
// along its via-path — never the calibrated/effective score, and never
// MMR's rewrite. Banking anything but raw evidence feeds the mass lift
// back into the next turn's mass (the measured echo: raw sim 0.36 banked
// as 0.99, re-lifted every turn) and would let MMR's provably-negative
// rewrites pollute the recorded graph.
func TestSelect_ProvenanceWeightIsRawEvidence(t *testing.T) {
	e := testEngine(newMockScorer(), newMockChunkOracle())

	// Chain m0 -e1-> m1 -e2-> m2 with the stored verdict deliberately
	// different from the raw CrossEncoderScore on every edge.
	e.dag.AddEdge(&rrcv1.Edge{
		FromMessageId: "m1", ToMessageId: "m2",
		Score: 0.8, CrossEncoderScore: 0.7,
		FromThreadId: "t1", ToThreadId: "t1",
	})
	e.dag.AddEdge(&rrcv1.Edge{
		FromMessageId: "m0", ToMessageId: "m1",
		Score: 0.95, CrossEncoderScore: 0.75,
		FromThreadId: "t1", ToThreadId: "t1",
	})

	result, err := e.Select("m2", threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*rrcv1.SelectedMessage{}
	for _, s := range result.Selected {
		got[s.MessageId] = s
	}
	m1, ok := got["m1"]
	if !ok {
		t.Fatal("m1 not selected")
	}
	// 1-hop: the accepting edge's raw CE, NOT the calibrated 0.8.
	if math.Abs(float64(m1.ProvenanceWeight)-0.7) > 1e-6 {
		t.Fatalf("m1 provenance_weight = %v, want raw 0.7 (stored verdict is 0.8)", m1.ProvenanceWeight)
	}
	m0, ok := got["m0"]
	if !ok {
		t.Fatal("m0 not selected")
	}
	// 2-hop: chain product of RAW evidence (0.7×0.75), while the effective
	// score factorizes per the perishable-inference law — the hop-1 entry
	// verdict (0.8, this event's own) × the hop-2 RELATIONAL strength
	// derived from the recorded observation under the current calibrator
	// (never the stored 0.95, a past event's verdict).
	if math.Abs(float64(m0.ProvenanceWeight)-0.525) > 1e-6 {
		t.Fatalf("m0 provenance_weight = %v, want 0.525 (raw chain product)", m0.ProvenanceWeight)
	}
	wantEffective := 0.8 * e.cfg.Calibrator.Predict(0.75, 0)
	if math.Abs(float64(m0.EffectiveScore)-wantEffective) > 1e-5 {
		t.Fatalf("m0 effective = %v, want %v (entry verdict × relational strength)", m0.EffectiveScore, wantEffective)
	}

	// MMR rewrites EffectiveScore (negative for near-duplicates) but must
	// leave the banked raw evidence untouched.
	o := newVectorOracle()
	o.set("anchor", []float32{1, 0, 0})
	o.set("near", []float32{1, 0, 0})
	mmrIn := []*rrcv1.SelectedMessage{
		{MessageId: "anchor", EffectiveScore: 0.95, ProvenanceWeight: 0.61},
		{MessageId: "near", EffectiveScore: 0.90, ProvenanceWeight: 0.59},
	}
	me := NewEngine(testConfig(), newMockScorer(), WithChunkOracle(o))
	out2, err := me.ApplyMMR(context.Background(), mmrIn)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range out2 {
		switch s.MessageId {
		case "near":
			if s.EffectiveScore >= 0.90 {
				t.Fatalf("near effective should be MMR-rewritten below 0.90, got %v", s.EffectiveScore)
			}
			if math.Abs(float64(s.ProvenanceWeight)-0.59) > 1e-6 {
				t.Fatalf("MMR rewrote provenance_weight: %v", s.ProvenanceWeight)
			}
		case "anchor":
			if math.Abs(float64(s.ProvenanceWeight)-0.61) > 1e-6 {
				t.Fatalf("anchor provenance_weight changed: %v", s.ProvenanceWeight)
			}
		}
	}
}

// TestNewEngine_RequiresEstimator pins the constructor invariant: an
// engine without a token estimator cannot exist. Every Assemble
// estimates (wire sizing runs before the budget check), so the
// failure belongs at construction, not mid-flight on the first
// message.
func TestNewEngine_RequiresEstimator(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewEngine without an estimator must panic")
		}
	}()
	NewEngine(DefaultConfig(), newMockScorer())
}

// TestEdgesCarryInstrumentIdentity pins A4-D5: both edge channels stamp
// the scorer whose units their observations are in — retroactively
// unrecoverable, so it must ride formation from day one.
func TestEdgesCarryInstrumentIdentity(t *testing.T) {
	mc := newMockScorer()
	mc.SetScore("a", "b", 0.9)
	o := newMockChunkOracle()
	cfg := testConfig()
	cfg.ScorerModelID = "scorer-x"
	cfg.MinBatchStdDev = 0 // single-candidate fixture: don't trip the flat-batch gate
	e := NewEngine(cfg, mc, WithChunkOracle(o))

	// Provenance channel.
	pEdges := e.RecordProvenance(
		&threadv1.Message{Id: "anchor", ThreadId: "t1"},
		[]Contributor{{MessageID: "c", ThreadID: "t1", Weight: 1.0}},
	)
	if len(pEdges) != 1 || pEdges[0].ScorerModel != "scorer-x" {
		t.Fatalf("provenance edge stamp: %+v", pEdges)
	}

	// Cross-encoder formation channel.
	msgs := []*threadv1.Message{
		addMsg(o, "m0", 0, "t1", "a"),
		addMsg(o, "m1", 1, "t1", "b"),
	}
	ceEdges, _, err := e.selectViaFixture(context.Background(), msgs[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(ceEdges) == 0 || ceEdges[0].ScorerModel != "scorer-x" {
		t.Fatalf("cross-encoder edge stamp: %+v", ceEdges)
	}
}
