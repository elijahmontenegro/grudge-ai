package rrc

import (
	"context"
	"errors"
	"testing"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- Mock implementations ---

// mockClassifier returns configurable reranker scores keyed by
// (prior candidate text, query text). The engine's Rerank call passes
// the new-chunk text as query and prior-chunk texts as candidates —
// keying the map as "candidate|query" matches the SetScore(prior, new)
// call convention used below.
type mockClassifier struct {
	pairScores map[string]float64
	callCount  int
}

func newMockClassifier() *mockClassifier {
	return &mockClassifier{pairScores: make(map[string]float64)}
}

// SetScore registers the reranker score the mock will return for a
// (prior, new) text pair. `prior` is the candidate text the reranker
// is scoring; `new` is the query text (the new-chunk being scored
// against its priors in OnMessage).
func (m *mockClassifier) SetScore(prior, new string, score float64) {
	m.pairScores[prior+"|"+new] = score
}

func (m *mockClassifier) Score(_ context.Context, query string, candidates []string) ([]float64, error) {
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
	texts map[string]string
}

func newMockChunkOracle() *mockChunkOracle {
	return &mockChunkOracle{texts: make(map[string]string)}
}

// Register records a message's text so subsequent ChunksForMessages
// calls return a chunk for it. Test helpers call this implicitly via
// addMsg() — direct callers can use it for edge cases.
func (o *mockChunkOracle) Register(messageID, text string) {
	o.texts[messageID] = text
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

// --- Helpers ---

func makeMsg(id string, position int64, threadID string, text string) *pb.Message {
	return &pb.Message{
		Id:        id,
		Role:      pb.Role_ROLE_USER,
		Content:   []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: text}}}},
		Position:  position,
		ThreadId:  threadID,
		CreatedAt: timestamppb.Now(),
	}
}

// addMsg is a convenience constructor that also registers the text
// with the oracle — every test that feeds messages to OnMessage must
// have the oracle know about them.
func addMsg(o *mockChunkOracle, id string, position int64, threadID string, text string) *pb.Message {
	m := makeMsg(id, position, threadID, text)
	o.Register(id, text)
	return m
}

// testEngine wires a fresh engine with the mock classifier and oracle.
// cfg defaults to DefaultConfig with the adaptive gates disabled
// (ZScoreThreshold=0, MinBatchStdDev=0) — legacy tests assert basic
// threshold gating without the adaptive layer, which has its own
// dedicated test suite further down.
func testEngine(mc *mockClassifier, o *mockChunkOracle) *Engine {
	cfg := DefaultConfig()
	cfg.ZScoreThreshold = 0
	cfg.MinBatchStdDev = 0
	return NewEngine(cfg, mc, WithChunkOracle(o))
}

// --- Tests ---

func TestNewEngine(t *testing.T) {
	cfg := DefaultConfig()
	e := NewEngine(cfg, newMockClassifier())

	if e.classifier == nil {
		t.Fatal("classifier should not be nil")
	}
	if e.cfg.EdgeThreshold != 0.5 {
		t.Fatalf("expected threshold 0.5, got %f", e.cfg.EdgeThreshold)
	}
}

func TestOnMessage_EmptyCorpus(t *testing.T) {
	e := testEngine(newMockClassifier(), newMockChunkOracle())
	msg := makeMsg("m1", 0, "t1", "hello")

	edges, err := e.OnMessage(context.Background(), msg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 {
		t.Fatalf("empty corpus should return no edges, got %d", len(edges))
	}
}

func TestOnMessage_NilClassifier(t *testing.T) {
	// A nil classifier is an infrastructure problem; the engine returns
	// ErrClassifierUnavailable so callers can surface it rather than
	// silently producing zero edges.
	o := newMockChunkOracle()
	e := NewEngine(DefaultConfig(), nil, WithChunkOracle(o))
	msg := addMsg(o, "m1", 1, "t1", "hello")
	corpus := []*pb.Message{addMsg(o, "m0", 0, "t1", "hi")}

	_, err := e.OnMessage(context.Background(), msg, corpus)
	if !errors.Is(err, ErrClassifierUnavailable) {
		t.Fatalf("expected ErrClassifierUnavailable, got %v", err)
	}
}

func TestOnMessage_NilOracle(t *testing.T) {
	// Symmetric to nil classifier: no oracle means OnMessage cannot
	// resolve chunks. Same failure class — surface, don't silently
	// produce zero edges.
	e := NewEngine(DefaultConfig(), newMockClassifier())
	msg := makeMsg("m1", 1, "t1", "hello")
	corpus := []*pb.Message{makeMsg("m0", 0, "t1", "hi")}

	_, err := e.OnMessage(context.Background(), msg, corpus)
	if !errors.Is(err, ErrClassifierUnavailable) {
		t.Fatalf("expected ErrClassifierUnavailable, got %v", err)
	}
}

func TestOnMessage_BelowThreshold_SameThread(t *testing.T) {
	// Three-gate discrimination replaced the old trajectory-fallback
	// behavior: below-EdgeThreshold fused scores produce no edge,
	// same-thread or otherwise. The structural temporal signal is
	// folded into the fused score via FuseScore, it doesn't bypass
	// the threshold.
	mc := newMockClassifier()
	mc.SetScore("hi", "hello", 0.3) // reranker 0.3
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	m0 := addMsg(o, "m0", 0, "t1", "hi")
	m1 := addMsg(o, "m1", 1, "t1", "hello")

	edges, err := e.OnMessage(context.Background(), m1, []*pb.Message{m0})
	if err != nil {
		t.Fatal(err)
	}
	// Fused = 0.6*0.3 + 0.4*0.5 = 0.18 + 0.2 = 0.38, below EdgeThreshold 0.5.
	if len(edges) != 0 {
		t.Fatalf("below-threshold fused score should produce no edge, got %d", len(edges))
	}
}

func TestOnMessage_BelowThreshold_CrossThread(t *testing.T) {
	mc := newMockClassifier()
	mc.SetScore("hi", "hello", 0.3)
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	m0 := addMsg(o, "m0", 0, "t-other", "hi")
	m1 := addMsg(o, "m1", 1, "t1", "hello")

	edges, err := e.OnMessage(context.Background(), m1, []*pb.Message{m0})
	if err != nil {
		t.Fatal(err)
	}
	// Cross-thread temporal proximity is effectively zero (positions
	// are thread-local). Fused = 0.6*0.3 + 0.4*~0 ≈ 0.18, below threshold.
	if len(edges) != 0 {
		t.Fatalf("cross-thread below-threshold pair should create no edge, got %d", len(edges))
	}
}

func TestOnMessage_AboveThreshold(t *testing.T) {
	mc := newMockClassifier()
	mc.SetScore("what is a tomato cake", "tell me more about tomato cake", 0.8)
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	m0 := addMsg(o, "m0", 0, "t1", "what is a tomato cake")
	m1 := addMsg(o, "m1", 1, "t1", "tell me more about tomato cake")

	edges, err := e.OnMessage(context.Background(), m1, []*pb.Message{m0})
	if err != nil {
		t.Fatal(err)
	}
	// Fused = 0.6*0.8 + 0.4*0.5 = 0.68. Above EdgeThreshold.
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
	if edge.Source != pb.EdgeSource_EDGE_SOURCE_CROSS_ENCODER {
		t.Fatalf("expected CE source, got %v", edge.Source)
	}
}

func TestOnMessage_MultipleCorpusMessages(t *testing.T) {
	mc := newMockClassifier()
	mc.SetScore("hello", "how are you", 0.7)
	mc.SetScore("nice weather", "how are you", 0.2)
	mc.SetScore("tell me a joke", "how are you", 0.6)
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	corpus := []*pb.Message{
		addMsg(o, "m0", 0, "t1", "hello"),
		addMsg(o, "m1", 1, "t1", "nice weather"),
		addMsg(o, "m2", 2, "t1", "tell me a joke"),
	}
	prompt := addMsg(o, "m3", 3, "t1", "how are you")

	edges, err := e.OnMessage(context.Background(), prompt, corpus)
	if err != nil {
		t.Fatal(err)
	}
	// Fused scores (CE=0.6, Temp=0.4 per DefaultConfig):
	//   m0 (d=3, temporal=0.25): 0.6*0.7 + 0.4*0.25 = 0.52  → above 0.5
	//   m1 (d=2, temporal=0.333): 0.6*0.2 + 0.4*0.333 = 0.253 → below
	//   m2 (d=1, temporal=0.5):   0.6*0.6 + 0.4*0.5 = 0.56   → above
	// With adaptive gates disabled by testEngine, just the absolute
	// threshold applies: 2 edges.
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges above threshold, got %d", len(edges))
	}
}

func TestSelect_NoEdges(t *testing.T) {
	e := testEngine(newMockClassifier(), newMockChunkOracle())

	result, err := e.Select("m0", pb.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) != 0 {
		t.Fatalf("expected empty selection for message with no edges, got %d", len(result.Selected))
	}
}

func TestSelect_LinearChain(t *testing.T) {
	mc := newMockClassifier()
	// Reranker scores; fused = 0.6*CE + 0.4*temporal (adjacent temporal=0.5).
	// Adjacent pairs: fused = 0.6*CE + 0.2.
	mc.SetScore("a", "b", 0.8) // fused 0.68
	mc.SetScore("b", "c", 0.7) // fused 0.62
	mc.SetScore("c", "d", 0.9) // fused 0.74
	mc.SetScore("a", "c", 0.3) // d=2 temporal=0.333 → fused 0.313
	mc.SetScore("a", "d", 0.2) // d=3 temporal=0.25  → fused 0.22
	mc.SetScore("b", "d", 0.3) // d=2 temporal=0.333 → fused 0.313
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	ctx := context.Background()
	msgs := []*pb.Message{
		addMsg(o, "m0", 0, "t1", "a"),
		addMsg(o, "m1", 1, "t1", "b"),
		addMsg(o, "m2", 2, "t1", "c"),
		addMsg(o, "m3", 3, "t1", "d"),
	}

	if _, err := e.OnMessage(ctx, msgs[1], msgs[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.OnMessage(ctx, msgs[2], msgs[:2]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.OnMessage(ctx, msgs[3], msgs[:3]); err != nil {
		t.Fatal(err)
	}

	result, err := e.Select("m3", pb.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
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

func TestSelect_ScoreFloorCutoff(t *testing.T) {
	// DAG-direct edge insert; independent of OnMessage path.
	e := testEngine(newMockClassifier(), newMockChunkOracle())

	// Below the ScoreFloor of DefaultConfig (0.3).
	e.dag.AddEdge(&pb.Edge{
		FromMessageId: "m0", ToMessageId: "m1",
		Score:        0.1,
		FromThreadId: "t1", ToThreadId: "t1",
	})

	result, err := e.Select("m1", pb.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) != 0 {
		t.Fatalf("scores below floor should be excluded, got %d selected", len(result.Selected))
	}
}

func TestSelect_ThreadScope(t *testing.T) {
	e := testEngine(newMockClassifier(), newMockChunkOracle())

	// Edges set CrossEncoderScore directly because extractSubgraph
	// re-projects Score from raw components under the current config.
	// CE=1.0 with Temp=0 → fused 0.6, above EdgeThreshold 0.5.
	e.dag.AddEdge(&pb.Edge{
		FromMessageId: "m0", ToMessageId: "m2",
		Score: 0.8, CrossEncoderScore: 1.0,
		FromThreadId: "t1", ToThreadId: "t1",
	})
	e.dag.AddEdge(&pb.Edge{
		FromMessageId: "m1", ToMessageId: "m2",
		Score: 0.9, CrossEncoderScore: 1.0,
		FromThreadId: "t2", ToThreadId: "t1",
	})

	result, err := e.Select("m2", pb.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) != 1 {
		t.Fatalf("thread scope should select 1 (not cross-thread), got %d", len(result.Selected))
	}
	if result.Selected[0].MessageId != "m0" {
		t.Fatalf("expected m0 selected, got %s", result.Selected[0].MessageId)
	}

	result, err = e.Select("m2", pb.SelectionScope_SELECTION_SCOPE_ALL_THREADS, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) != 2 {
		t.Fatalf("all-threads scope should select 2, got %d", len(result.Selected))
	}
}

func TestFuseScore(t *testing.T) {
	cfg := DefaultConfig()
	// Same-thread, defaults WeightCE=0.6, WeightTemp=0.4:
	//   0.6*0.8 + 0.4*0.5 = 0.48 + 0.2 = 0.68
	score := FuseScore(cfg, 0.8, 0.5, false)
	if diff := score - 0.68; diff > 0.001 || diff < -0.001 {
		t.Fatalf("same-thread: expected 0.68, got %f", score)
	}
}

func TestFuseScore_CEOnly(t *testing.T) {
	cfg := DefaultConfig()
	// Same-thread zero temporal: 0.6*0.6 + 0.4*0 = 0.36
	score := FuseScore(cfg, 0.6, 0, false)
	if diff := score - 0.36; diff > 0.001 || diff < -0.001 {
		t.Fatalf("same-thread zero-temporal: expected 0.36, got %f", score)
	}
}

func TestFuseScore_CrossThread(t *testing.T) {
	cfg := DefaultConfig()
	// Cross-thread: temporal is meaningless across threads, so the
	// fused score is the raw rerank. This levels the gating with
	// same-thread (both clear EdgeThreshold=0.5 at CE=0.83 same-
	// thread-zero-temporal vs CE=0.5 cross-thread).
	score := FuseScore(cfg, 0.7, 0.0, true)
	if diff := score - 0.7; diff > 0.001 || diff < -0.001 {
		t.Fatalf("cross-thread: expected 0.7 (raw rerank), got %f", score)
	}
	// Even with a non-zero temporal value (which shouldn't happen
	// but isn't an error), cross-thread ignores it.
	score = FuseScore(cfg, 0.6, 0.5, true)
	if diff := score - 0.6; diff > 0.001 || diff < -0.001 {
		t.Fatalf("cross-thread temporal-ignored: expected 0.6, got %f", score)
	}
}

func TestTemporalProximity(t *testing.T) {
	tests := []struct {
		posA, posB int64
		expected   float64
	}{
		{0, 1, 0.5},
		{0, 0, 1.0},
		{0, 9, 0.1},
		{0, 99, 0.01},
		{5, 3, 0.333},
	}
	for _, tt := range tests {
		got := TemporalProximity(tt.posA, tt.posB)
		if diff := got - tt.expected; diff > 0.01 || diff < -0.01 {
			t.Errorf("TemporalProximity(%d,%d) = %f, want ~%f", tt.posA, tt.posB, got, tt.expected)
		}
	}
}

func TestFork(t *testing.T) {
	mc := newMockClassifier()
	mc.SetScore("hello", "world", 0.8)
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	m0 := addMsg(o, "m0", 0, "t1", "hello")
	m1 := addMsg(o, "m1", 1, "t1", "world")
	if _, err := e.OnMessage(context.Background(), m1, []*pb.Message{m0}); err != nil {
		t.Fatal(err)
	}

	fork := e.Fork()

	parentEdges := e.dag.AllEdges()
	forkEdges := fork.dag.AllEdges()
	if len(forkEdges) != len(parentEdges) {
		t.Fatalf("fork should have %d edges, got %d", len(parentEdges), len(forkEdges))
	}

	if fork.classifier != e.classifier {
		t.Fatal("fork should share classifier reference")
	}
}

func TestMerge(t *testing.T) {
	mc := newMockClassifier()
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	e.dag.AddEdge(&pb.Edge{
		FromMessageId: "m0", ToMessageId: "m1",
		Score: 0.8, FromThreadId: "t1", ToThreadId: "t1",
	})

	fork := e.Fork()

	fork.dag.AddEdge(&pb.Edge{
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
	edge := &pb.Edge{
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

	// Score cache is chunk-granular: key is (fromMsgID, fromChunkIdx,
	// toMsgID, toChunkIdx). Messages with a single chunk use index 0.
	sc.set("a", 0, "b", 0, 0.75)

	score, ok := sc.get("a", 0, "b", 0)
	if !ok || score != 0.75 {
		t.Fatalf("expected 0.75, got %f (ok=%v)", score, ok)
	}

	if _, ok := sc.get("b", 0, "a", 0); ok {
		t.Fatal("reverse direction should not be cached")
	}

	if _, ok := sc.get("x", 0, "y", 0); ok {
		t.Fatal("unknown pair should return false")
	}

	if _, ok := sc.get("a", 1, "b", 0); ok {
		t.Fatal("different chunk index should not be cached")
	}
}

func TestSelect_TransitiveReduction(t *testing.T) {
	e := testEngine(newMockClassifier(), newMockChunkOracle())

	// Diamond: m0 -> m2, m0 -> m1 -> m2. Direct m0->m2 is redundant.
	// CrossEncoderScore set directly because extractSubgraph
	// re-projects under current config.
	e.dag.AddEdge(&pb.Edge{
		FromMessageId: "m0", ToMessageId: "m2",
		Score: 0.6, CrossEncoderScore: 1.0,
		FromThreadId: "t1", ToThreadId: "t1",
	})
	e.dag.AddEdge(&pb.Edge{
		FromMessageId: "m1", ToMessageId: "m2",
		Score: 0.8, CrossEncoderScore: 1.0,
		FromThreadId: "t1", ToThreadId: "t1",
	})
	e.dag.AddEdge(&pb.Edge{
		FromMessageId: "m0", ToMessageId: "m1",
		Score: 0.7, CrossEncoderScore: 1.0,
		FromThreadId: "t1", ToThreadId: "t1",
	})

	result, err := e.Select("m2", pb.SelectionScope_SELECTION_SCOPE_THREAD, "t1")
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Selected) != 2 {
		t.Fatalf("expected 2 selected, got %d", len(result.Selected))
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.EdgeThreshold != 0.5 {
		t.Fatalf("expected threshold 0.5, got %f", cfg.EdgeThreshold)
	}
	if cfg.WeightCE != 0.6 {
		t.Fatalf("expected CE weight 0.6, got %f", cfg.WeightCE)
	}
	if cfg.WeightTemp != 0.4 {
		t.Fatalf("expected temp weight 0.4, got %f", cfg.WeightTemp)
	}
	if cfg.ScoreFloor != 0.3 {
		t.Fatalf("expected floor 0.3, got %f", cfg.ScoreFloor)
	}
	sum := cfg.WeightCE + cfg.WeightTemp
	if diff := sum - 1.0; diff > 0.001 || diff < -0.001 {
		t.Fatalf("weights should sum to 1.0, got %f", sum)
	}
}

func TestOnMessage_ScoreCachePopulated(t *testing.T) {
	mc := newMockClassifier()
	mc.SetScore("a", "b", 0.3) // reranker 0.3 — below the EdgeThreshold fused cutoff
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	m0 := addMsg(o, "m0", 0, "t1", "a")
	m1 := addMsg(o, "m1", 1, "t1", "b")

	if _, err := e.OnMessage(context.Background(), m1, []*pb.Message{m0}); err != nil {
		t.Fatal(err)
	}

	// Score cache keyed at chunk granularity (both messages have one
	// chunk at index 0). The reranker score is cached even when no
	// edge was emitted — future OnMessage calls touching this pair
	// skip the reranker entirely.
	score, ok := e.scores.get("m0", 0, "m1", 0)
	if !ok {
		t.Fatal("score should be cached")
	}
	if diff := score - 0.3; diff > 0.01 || diff < -0.01 {
		t.Fatalf("cached score should be ~0.3, got %f", score)
	}
}

func TestOnMessage_SkipsSelf(t *testing.T) {
	mc := newMockClassifier()
	o := newMockChunkOracle()
	e := testEngine(mc, o)

	m0 := addMsg(o, "m0", 0, "t1", "hello")
	// Corpus includes the message itself — should be filtered out and
	// no classifier call should happen.
	edges, err := e.OnMessage(context.Background(), m0, []*pb.Message{m0})
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 {
		t.Fatal("should not create edges to self")
	}
	if mc.callCount != 0 {
		t.Fatal("should not call classifier when only self in corpus")
	}
}

// --- Three-gate discrimination tests ---
//
// Gates stack:
//   Gate 1 (absolute): fused < EdgeThreshold → skip
//   Gate 2 (z-score):  (fused - batchMean) / batchStddev < ZScoreThreshold → skip
//   Gate 3 (batch):    batchStddev < MinBatchStdDev → zero edges for the whole batch
//
// Batch stats are computed over candidates that actually got rescored
// (either fresh Rerank call or cached score), not over priors that
// fell outside RerankTopK with no cache hit. That invariant has its
// own dedicated test below — without it, zero-score synthetic entries
// would distort mean/stddev and fire the gates against the wrong
// baseline.

// threeGateConfig enables the adaptive gates that testEngine disables.
// Priors at position 0 (various threads), query at position 0 too —
// temporal proximity = 1.0 for every pair, so fused = 0.6*CE + 0.4
// and we can drive the distribution purely via mock CE scores.
func threeGateConfig() EngineConfig {
	cfg := DefaultConfig()
	cfg.ZScoreThreshold = 1.0
	cfg.MinBatchStdDev = 0.05
	return cfg
}

func threeGateEngine(mc *mockClassifier, o *mockChunkOracle) *Engine {
	return NewEngine(threeGateConfig(), mc, WithChunkOracle(o))
}

func TestOnMessage_Gate1_AbsoluteThreshold(t *testing.T) {
	// Two priors: one clears 0.5, one doesn't. With cross-thread
	// priors at pos 0 and query at pos 0, temporal=1.0 → fused =
	// 0.6*CE + 0.4.
	//   CE=0.1 → fused=0.46 (below threshold)
	//   CE=0.5 → fused=0.70 (above threshold)
	// Note CE=0.1 rather than 0.0: the engine's aggregation uses a
	// strict `>` against zero-init, so CE=0.0 is treated as unscored
	// and the candidate never enters the batch. Any non-zero CE
	// below the threshold exercises gate 1 correctly.
	mc := newMockClassifier()
	mc.SetScore("low", "query", 0.1)
	mc.SetScore("high", "query", 0.5)
	o := newMockChunkOracle()
	e := threeGateEngine(mc, o)

	m0 := addMsg(o, "m0", 0, "tA", "low")
	m1 := addMsg(o, "m1", 0, "tB", "high")
	q := addMsg(o, "q", 0, "tQ", "query")

	edges, err := e.OnMessage(context.Background(), q, []*pb.Message{m0, m1})
	if err != nil {
		t.Fatal(err)
	}
	// Batch stats over [0.46, 0.70]: mean=0.58, stddev=0.12.
	// Gate 3: 0.12 > 0.05 — no fire.
	// Gate 1: 0.46 < 0.5 fails, 0.70 passes.
	// Gate 2: z(0.70) = (0.70-0.58)/0.12 = 1.0 exactly; `z<1.0` is
	// false, so high passes gate 2 too.
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (low gated by gate 1), got %d", len(edges))
	}
	if edges[0].FromMessageId != "m1" {
		t.Fatalf("wrong prior emitted an edge: %s", edges[0].FromMessageId)
	}
}

func TestOnMessage_CrossThreadEdgeThreshold(t *testing.T) {
	// Cross-thread fused score = raw CE (FuseScore short-circuits
	// the temporal term). At default same-thread EdgeThreshold=0.5,
	// a cross-thread CE in the 0.40-0.50 band — the band where bge-
	// reranker-v2-m3 lands genuinely-related cross-thread material —
	// would be silently rejected.
	//
	// CrossThreadEdgeThreshold separates the gate. With it set to
	// 0.4, a cross-thread CE=0.45 forms an edge; flipping it back to
	// 0.5 (single-knob legacy) suppresses it.
	cfg := DefaultConfig()
	cfg.ZScoreThreshold = 0
	cfg.MinBatchStdDev = 0
	cfg.EdgeThreshold = 0.5
	cfg.CrossThreadEdgeThreshold = 0.4

	mc := newMockClassifier()
	mc.SetScore("borderline", "q", 0.45)
	o := newMockChunkOracle()
	e := NewEngine(cfg, mc, WithChunkOracle(o))

	m0 := addMsg(o, "m0", 0, "tA", "borderline")
	q := addMsg(o, "q", 0, "tQ", "q")

	edges, err := e.OnMessage(context.Background(), q, []*pb.Message{m0})
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("cross-thread CE=0.45 should clear CrossThreadEdgeThreshold=0.4, got %d edges", len(edges))
	}
	// Sanity: same CE on a same-thread prior would clear neither
	// threshold (fused = 0.6*0.45 + 0.4*1.0 = 0.67 — actually clears
	// here because temporal=1.0). Use a positionally-distant same-
	// thread prior so temporal contribution drops.
	mc2 := newMockClassifier()
	mc2.SetScore("borderline-same", "q2", 0.45)
	o2 := newMockChunkOracle()
	e2 := NewEngine(cfg, mc2, WithChunkOracle(o2))
	pSame := addMsg(o2, "p", 0, "tQ", "borderline-same")
	q2 := addMsg(o2, "q2", 100, "tQ", "q2") // d=100, temporal=1/101 ≈ 0.0099
	sameEdges, err := e2.OnMessage(context.Background(), q2, []*pb.Message{pSame})
	if err != nil {
		t.Fatal(err)
	}
	// Same-thread fused = 0.6*0.45 + 0.4*0.0099 ≈ 0.274 < 0.5 → no edge.
	if len(sameEdges) != 0 {
		t.Fatalf("same-thread CE=0.45 with weak temporal should not clear EdgeThreshold=0.5, got %d edges", len(sameEdges))
	}

	// Now disable the cross-thread carve-out and confirm legacy
	// behavior: cross-thread CE=0.45 is suppressed under 0.5.
	cfg.CrossThreadEdgeThreshold = 0 // fall through to EdgeThreshold
	mc3 := newMockClassifier()
	mc3.SetScore("borderline", "q", 0.45)
	o3 := newMockChunkOracle()
	e3 := NewEngine(cfg, mc3, WithChunkOracle(o3))
	m0b := addMsg(o3, "m0b", 0, "tA", "borderline")
	qb := addMsg(o3, "qb", 0, "tQ", "q")
	legacyEdges, err := e3.OnMessage(context.Background(), qb, []*pb.Message{m0b})
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyEdges) != 0 {
		t.Fatalf("CrossThreadEdgeThreshold=0 should fall through to EdgeThreshold=0.5; CE=0.45 cross-thread should be suppressed, got %d edges", len(legacyEdges))
	}
}

func TestOnMessage_Gate2_ZScoreBlocksCluster(t *testing.T) {
	// Three priors all above absolute threshold. Two form a cluster,
	// one is a clear outlier. Gate 2 keeps the outlier only — the
	// cluster z-scores sit below ZScoreThreshold.
	//   CE=0.25 → fused=0.55  (x2, cluster)
	//   CE=0.50 → fused=0.70  (outlier)
	mc := newMockClassifier()
	mc.SetScore("a", "q", 0.25)
	mc.SetScore("b", "q", 0.25)
	mc.SetScore("c", "q", 0.50)
	o := newMockChunkOracle()
	e := threeGateEngine(mc, o)

	m0 := addMsg(o, "m0", 0, "tA", "a")
	m1 := addMsg(o, "m1", 0, "tB", "b")
	m2 := addMsg(o, "m2", 0, "tC", "c")
	q := addMsg(o, "q", 0, "tQ", "q")

	edges, err := e.OnMessage(context.Background(), q, []*pb.Message{m0, m1, m2})
	if err != nil {
		t.Fatal(err)
	}
	// Batch [0.55, 0.55, 0.70]: mean=0.60, stddev=sqrt(0.005)=0.0707.
	// Gate 3: 0.0707 > 0.05 — no fire.
	// Gate 1: all >= 0.5 — all pass.
	// Gate 2: z(0.55) = -0.707 < 1.0 — skipped. z(0.70) = 1.414 — passes.
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (outlier only, cluster blocked by gate 2), got %d", len(edges))
	}
	if edges[0].FromMessageId != "m2" {
		t.Fatalf("expected outlier m2 to emit edge, got %s", edges[0].FromMessageId)
	}
}

func TestOnMessage_Gate2_Disabled(t *testing.T) {
	// ZScoreThreshold=0 disables the gate — cluster candidates survive.
	cfg := threeGateConfig()
	cfg.ZScoreThreshold = 0
	mc := newMockClassifier()
	mc.SetScore("a", "q", 0.25)
	mc.SetScore("b", "q", 0.25)
	mc.SetScore("c", "q", 0.50)
	o := newMockChunkOracle()
	e := NewEngine(cfg, mc, WithChunkOracle(o))

	// Same-thread setup: with positions=0 all temporal=1.0, fused
	// = 0.6*CE + 0.4 lands at 0.55 / 0.55 / 0.70. Cross-thread
	// would skip temporal entirely (FuseScore semantics), so the
	// fused floor would drop below EdgeThreshold for the 0.25
	// candidates — that's a separate test surface; this one is
	// about gate behavior given clean fused scores.
	m0 := addMsg(o, "m0", 0, "tQ", "a")
	m1 := addMsg(o, "m1", 0, "tQ", "b")
	m2 := addMsg(o, "m2", 0, "tQ", "c")
	q := addMsg(o, "q", 0, "tQ", "q")

	edges, err := e.OnMessage(context.Background(), q, []*pb.Message{m0, m1, m2})
	if err != nil {
		t.Fatal(err)
	}
	// All three clear gate 1; gate 2 disabled; gate 3 stddev > 0.05.
	// Expect 3 edges.
	if len(edges) != 3 {
		t.Fatalf("gate 2 disabled should pass all gate-1 clearers, got %d", len(edges))
	}
}

func TestOnMessage_Gate3_BatchIndiscriminate(t *testing.T) {
	// Tight cluster of above-threshold candidates — stddev falls
	// below MinBatchStdDev. Gate 3 fires for the whole batch: zero
	// edges even though every candidate clears gate 1.
	//   CE=0.183 → fused=0.51
	//   CE=0.200 → fused=0.52
	//   CE=0.217 → fused=0.53
	mc := newMockClassifier()
	mc.SetScore("a", "q", 0.183)
	mc.SetScore("b", "q", 0.200)
	mc.SetScore("c", "q", 0.217)
	o := newMockChunkOracle()
	e := threeGateEngine(mc, o)

	m0 := addMsg(o, "m0", 0, "tA", "a")
	m1 := addMsg(o, "m1", 0, "tB", "b")
	m2 := addMsg(o, "m2", 0, "tC", "c")
	q := addMsg(o, "q", 0, "tQ", "q")

	edges, err := e.OnMessage(context.Background(), q, []*pb.Message{m0, m1, m2})
	if err != nil {
		t.Fatal(err)
	}
	// Batch [0.51, 0.52, 0.53]: stddev ≈ 0.00816 < MinBatchStdDev.
	// Gate 3 short-circuits → no edges.
	if len(edges) != 0 {
		t.Fatalf("tight cluster must return zero edges via gate 3, got %d", len(edges))
	}
}

func TestOnMessage_Gate3_Disabled(t *testing.T) {
	// MinBatchStdDev=0 disables gate 3 — tight cluster still has to
	// pass gates 1 and 2. With all three so close, z-scores all sit
	// below 1.0 except the max. Expect exactly 1 edge (top of
	// cluster clears gate 2 with z = sqrt(3/2) ≈ 1.22).
	cfg := threeGateConfig()
	cfg.MinBatchStdDev = 0
	mc := newMockClassifier()
	mc.SetScore("a", "q", 0.183)
	mc.SetScore("b", "q", 0.200)
	mc.SetScore("c", "q", 0.217)
	o := newMockChunkOracle()
	e := NewEngine(cfg, mc, WithChunkOracle(o))

	// Same-thread setup so temporal=1.0 contributes to fused, lifting
	// the tight CE cluster (0.51 / 0.52 / 0.53) just over EdgeThreshold.
	// Cross-thread would skip temporal and the cluster would all sit
	// below the threshold — different test surface.
	m0 := addMsg(o, "m0", 0, "tQ", "a")
	m1 := addMsg(o, "m1", 0, "tQ", "b")
	m2 := addMsg(o, "m2", 0, "tQ", "c")
	q := addMsg(o, "q", 0, "tQ", "q")

	edges, err := e.OnMessage(context.Background(), q, []*pb.Message{m0, m1, m2})
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (top of cluster clears gate 2), got %d", len(edges))
	}
	if edges[0].FromMessageId != "m2" {
		t.Fatalf("top-of-cluster should be m2, got %s", edges[0].FromMessageId)
	}
}

func TestOnMessage_Gate2_SingleCandidateNoOp(t *testing.T) {
	// Single-candidate batch has stddev=0 — gate 2's `batchStddev > 0`
	// guard makes it a no-op. Gate 3 would normally fire on stddev=0
	// but here MinBatchStdDev=0 in this test to isolate the single-
	// candidate case.
	cfg := threeGateConfig()
	cfg.MinBatchStdDev = 0
	mc := newMockClassifier()
	mc.SetScore("a", "q", 0.5) // fused = 0.6*0.5 + 0.4 = 0.7
	o := newMockChunkOracle()
	e := NewEngine(cfg, mc, WithChunkOracle(o))

	m0 := addMsg(o, "m0", 0, "tA", "a")
	q := addMsg(o, "q", 0, "tQ", "q")

	edges, err := e.OnMessage(context.Background(), q, []*pb.Message{m0})
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("single-candidate should pass gate 2 (no-op), got %d edges", len(edges))
	}
}

func TestOnMessage_RescoredFilterInvariant(t *testing.T) {
	// Batch stats must be computed over rescored priors only. Priors
	// beyond RerankTopK with no cached score contribute no fused
	// score and must be excluded from mean/stddev — otherwise synthetic
	// zeros depress the mean and inflate stddev, and the gates fire
	// against the wrong baseline.
	//
	// Setup: 5 priors with CE that would produce fused [0.68, 0.62,
	// 0, 0, 0] if ALL were counted (broken path), or [0.68, 0.62] if
	// only the first two are scored (correct path). RerankTopK=2.
	//
	// Correct path: stddev of [0.68, 0.62] = 0.03, below MinBatchStdDev
	// (0.05) → gate 3 fires → 0 edges.
	// Broken path: stddev of [0.68, 0.62, 0, 0, 0] ≈ 0.294 → gate 3
	// no-fire, gate 1 passes top two, gate 2 z=1.43 and 1.22 both
	// pass → 2 edges.
	cfg := threeGateConfig()
	cfg.RerankTopK = 2

	mc := newMockClassifier()
	// Temporal for adjacent positions (d=1) = 0.5; fused = 0.6*CE + 0.2.
	mc.SetScore("a", "q", 0.8) // fused = 0.68
	mc.SetScore("b", "q", 0.7) // fused = 0.62
	mc.SetScore("c", "q", 0.6) // not scored under RerankTopK=2
	mc.SetScore("d", "q", 0.5) // not scored
	mc.SetScore("e", "q", 0.4) // not scored

	o := newMockChunkOracle()
	e := NewEngine(cfg, mc, WithChunkOracle(o))

	priors := []*pb.Message{
		addMsg(o, "m0", 0, "t1", "a"),
		addMsg(o, "m1", 1, "t1", "b"),
		addMsg(o, "m2", 2, "t1", "c"),
		addMsg(o, "m3", 3, "t1", "d"),
		addMsg(o, "m4", 4, "t1", "e"),
	}
	q := addMsg(o, "q", 5, "t1", "q")

	edges, err := e.OnMessage(context.Background(), q, priors)
	if err != nil {
		t.Fatal(err)
	}
	// Rescored-filter invariant holds → gate 3 fires on the narrow
	// two-element batch → zero edges.
	if len(edges) != 0 {
		t.Fatalf("rescored-filter invariant broken: expected 0 edges (gate 3 fires on narrow batch), got %d", len(edges))
	}
}

func TestOnMessage_CachedScoresCountAsRescored(t *testing.T) {
	// A prior with a cached score counts as "rescored" for batch
	// aggregation — no fresh Rerank call needed, but it still enters
	// candidates and batch stats. Verifies the aggregation path
	// (cached OR in rerankSet) per engine.go lines 287-297.
	cfg := threeGateConfig()
	cfg.MinBatchStdDev = 0 // isolate the aggregation path

	mc := newMockClassifier()
	mc.SetScore("a", "q", 0.8)
	mc.SetScore("b", "q", 0.5)

	o := newMockChunkOracle()
	e := NewEngine(cfg, mc, WithChunkOracle(o))

	m0 := addMsg(o, "m0", 0, "t1", "a")
	m1 := addMsg(o, "m1", 1, "t1", "b")
	q := addMsg(o, "q", 2, "t1", "q")

	// Pre-seed the score cache for the m0→q pair. The engine should
	// see the cached value instead of calling Rerank on this pair.
	e.scores.set("m0", 0, "q", 0, 0.8)

	_, err := e.OnMessage(context.Background(), q, []*pb.Message{m0, m1})
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

func (o *vectorOracle) EnsureVector(_ context.Context, ref ChunkRef) ([]float32, error) {
	if v, ok := o.vectors[ref.MessageID]; ok {
		return v, nil
	}
	return nil, nil
}

// TestApplyMMR_ReordersNearDuplicates verifies the load-bearing
// assertion of the Phase B.1 design: given a near-duplicate chain of
// high-scoring candidates (the observed "nine copies of let me check
// the chapter" pattern) and one distinct lower-scoring candidate,
// MMR penalizes the duplicates enough that the distinct one emerges
// above most of them in the reordered output.
func TestApplyMMR_ReordersNearDuplicates(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DiversityLambda = 0.7

	o := newVectorOracle()
	// dup1..dup3 are near-identical vectors (cosine ≈ 1).
	// distinct is orthogonal.
	o.set("dup1", []float32{1, 0, 0})
	o.set("dup2", []float32{0.99, 0.01, 0})
	o.set("dup3", []float32{0.98, 0.02, 0})
	o.set("distinct", []float32{0, 1, 0})
	e := NewEngine(cfg, newMockClassifier(), WithChunkOracle(o))

	selected := []*pb.SelectedMessage{
		{MessageId: "dup1", EffectiveScore: 0.95},
		{MessageId: "dup2", EffectiveScore: 0.94},
		{MessageId: "dup3", EffectiveScore: 0.93},
		{MessageId: "distinct", EffectiveScore: 0.60},
	}

	out, err := e.ApplyMMR(context.Background(), selected, cfg.DiversityLambda)
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
	// Second pick should be distinct (orthogonal → diversity bonus
	// overwhelms the orig-score gap). Under λ=0.7:
	//   dup2 effective = 0.7*0.94 - 0.3*~0.99 ≈ 0.36
	//   distinct effective = 0.7*0.60 - 0.3*0   = 0.42
	if out[1].MessageId != "distinct" {
		t.Errorf("second pick should be distinct (diversity bonus beats orig-score gap); got %s", out[1].MessageId)
	}
	// Scores of non-first picks must be the MMR-adjusted values (lower
	// than orig), not the originals — otherwise downstream shed would
	// behave as if MMR never ran.
	for i := 1; i < len(out); i++ {
		for _, s := range selected {
			if s.MessageId == out[i].MessageId && float64(out[i].EffectiveScore) >= float64(s.EffectiveScore) {
				t.Errorf("MMR-adjusted score for %s (%.3f) should be below original (%.3f)",
					out[i].MessageId, out[i].EffectiveScore, s.EffectiveScore)
			}
		}
	}
}

// TestApplyMMR_NoOracleError verifies ApplyMMR fails loud rather than
// silently pass-through when the oracle isn't wired — an oracle-less
// engine running MMR is a configuration bug, not an acceptable
// degradation mode.
func TestApplyMMR_NoOracleError(t *testing.T) {
	e := NewEngine(DefaultConfig(), newMockClassifier())
	// No SetChunkOracle call.
	_, err := e.ApplyMMR(context.Background(), []*pb.SelectedMessage{
		{MessageId: "a", EffectiveScore: 0.5},
		{MessageId: "b", EffectiveScore: 0.4},
	}, 0.7)
	if err == nil {
		t.Fatal("expected ApplyMMR error without oracle wired")
	}
}

// TestApplyMMR_LambdaExtremesNoOp verifies that λ at the boundary
// values (0, 1) is treated as a no-op pass-through. Callers should
// un-wire the diversity penalty explicitly rather than pay the cost
// of computing "λ·x + 0" or "0 + (1-λ)·diversity" — those extremes
// collapse to the non-MMR paths the caller already has.
func TestApplyMMR_LambdaExtremesNoOp(t *testing.T) {
	e := NewEngine(DefaultConfig(), newMockClassifier(), WithChunkOracle(newVectorOracle()))
	selected := []*pb.SelectedMessage{
		{MessageId: "a", EffectiveScore: 0.9},
		{MessageId: "b", EffectiveScore: 0.8},
	}
	for _, lambda := range []float64{0.0, 1.0} {
		out, err := e.ApplyMMR(context.Background(), selected, lambda)
		if err != nil {
			t.Fatalf("lambda=%.1f: unexpected error: %v", lambda, err)
		}
		if len(out) != 2 || out[0].MessageId != "a" || out[1].MessageId != "b" {
			t.Errorf("lambda=%.1f: expected pass-through, got %v", lambda, out)
		}
		if out[0].EffectiveScore != 0.9 || out[1].EffectiveScore != 0.8 {
			t.Errorf("lambda=%.1f: scores must not be rewritten on pass-through", lambda)
		}
	}
}

// --- Composite NLI fusion (Phase B.3) ---

type mockEntailer struct {
	scores map[string]float64 // keyed by candidate text
	calls  int
	err    error
}

func (m *mockEntailer) Score(_ context.Context, _ string, hypotheses []string) ([]float64, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	out := make([]float64, len(hypotheses))
	for i, h := range hypotheses {
		out[i] = m.scores[h]
	}
	return out, nil
}

// TestOnMessage_NLIFusion verifies that when an entailer is wired,
// the raw reranker score is fused with the NLI score before becoming
// the edge score. Under α=0.5 with reranker=0.8 and NLI=0.4, the
// fused value is 0.6 — neither the raw rerank nor the raw NLI.
func TestOnMessage_NLIFusion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ZScoreThreshold = 0
	cfg.MinBatchStdDev = 0
	cfg.NLIFusionWeight = 0.5
	cfg.EdgeThreshold = 0.0 // let every edge form so we can inspect scores

	mc := newMockClassifier()
	// Rerank puts "prior" at 0.8 against query "q".
	mc.SetScore("prior", "q", 0.8)

	me := &mockEntailer{scores: map[string]float64{"prior": 0.4}}

	o := newMockChunkOracle()
	e := NewEngine(cfg, mc, WithChunkOracle(o), WithEntailer(me))

	prior := addMsg(o, "m0", 0, "t1", "prior")
	q := addMsg(o, "q", 1, "t1", "q")

	edges, err := e.OnMessage(context.Background(), q, []*pb.Message{prior})
	if err != nil {
		t.Fatalf("OnMessage error: %v", err)
	}
	if me.calls != 1 {
		t.Errorf("expected 1 Entail call, got %d", me.calls)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	// FuseScore combines CE score with temporal proximity, so we
	// only verify the CE field itself — that's the fused component.
	// α=0.5: 0.5*0.8 + 0.5*0.4 = 0.6.
	wantCE := 0.6
	got := float64(edges[0].CrossEncoderScore)
	if diff := got - wantCE; diff < -1e-6 || diff > 1e-6 {
		t.Errorf("fused reranker+NLI CE score: got %.4f want %.4f (α=0.5, rerank=0.8, nli=0.4)",
			got, wantCE)
	}
}

// TestOnMessage_NLIFailurePropagates — when an entailer IS wired but
// its call fails, the whole OnMessage round aborts per the classifier-
// fail-loud policy. A silent skip would leave the composite pipeline
// running one-legged without the operator knowing.
func TestOnMessage_NLIFailurePropagates(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ZScoreThreshold = 0
	cfg.MinBatchStdDev = 0
	cfg.NLIFusionWeight = 0.5

	mc := newMockClassifier()
	mc.SetScore("prior", "q", 0.8)

	me := &mockEntailer{err: errors.New("predict server down")}

	o := newMockChunkOracle()
	e := NewEngine(cfg, mc, WithChunkOracle(o), WithEntailer(me))

	prior := addMsg(o, "m0", 0, "t1", "prior")
	q := addMsg(o, "q", 1, "t1", "q")

	_, err := e.OnMessage(context.Background(), q, []*pb.Message{prior})
	if err == nil {
		t.Fatal("expected NLI failure to propagate, got nil error")
	}
	if !errors.Is(err, ErrClassifierFailed) {
		t.Errorf("expected ErrClassifierFailed wrap, got %v", err)
	}
}

// TestOnMessage_NLISkippedWhenUnwired — when no entailer is wired,
// OnMessage behaves exactly like the pre-composite path: raw rerank
// scores flow through without any fusion, regardless of
// NLIFusionWeight's value.
func TestOnMessage_NLISkippedWhenUnwired(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ZScoreThreshold = 0
	cfg.MinBatchStdDev = 0
	cfg.NLIFusionWeight = 0.5 // set but no entailer
	cfg.EdgeThreshold = 0.0

	mc := newMockClassifier()
	mc.SetScore("prior", "q", 0.8)

	o := newMockChunkOracle()
	e := NewEngine(cfg, mc, WithChunkOracle(o))
	// No entailer wired.

	prior := addMsg(o, "m0", 0, "t1", "prior")
	q := addMsg(o, "q", 1, "t1", "q")

	edges, err := e.OnMessage(context.Background(), q, []*pb.Message{prior})
	if err != nil {
		t.Fatalf("OnMessage error: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	// No fusion → CE score equals the raw rerank output.
	if diff := float64(edges[0].CrossEncoderScore) - 0.8; diff < -1e-6 || diff > 1e-6 {
		t.Errorf("without entailer, CE score should equal raw rerank 0.8; got %.4f",
			edges[0].CrossEncoderScore)
	}
}

