package adkbridge

import (
	"context"
	"errors"
	"iter"
	"testing"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/proto/pbtext"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/service/storage"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type captureCompleter struct {
	requests []*llmv1.CompletionRequest
	overflow bool
}

func (c *captureCompleter) Complete(_ context.Context, req *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error) {
	c.requests = append(c.requests, req)
	if c.overflow {
		c.overflow = false
		return nil, errors.New("context length exceeded")
	}
	return &llmv1.CompletionResponse{
		Message: &llmv1.LLMMessage{Role: threadv1.Role_ROLE_ASSISTANT, Content: pbtext.BlocksFromText("ok")},
	}, nil
}

func (c *captureCompleter) Stream(context.Context, *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error] {
	return func(func(*llmv1.StreamChunk, error) bool) {}
}

type fixedScorer struct {
	calls int
}

func (s *fixedScorer) Score(context.Context, string, []string) ([]float64, error) {
	s.calls++
	return []float64{0.9}, nil
}

type fixedOracle struct {
	candidate rrc.ChunkRef
}

func (o fixedOracle) ChunksForMessages(context.Context, []string) (map[string][]rrc.ChunkRef, error) {
	return nil, nil
}
func (o fixedOracle) EnsureVector(context.Context, rrc.ChunkRef) ([]float32, error) {
	return nil, nil
}
func (o fixedOracle) NearestChunks(context.Context, string, int, rrc.Predicate) ([]rrc.ChunkRef, error) {
	return []rrc.ChunkRef{o.candidate}, nil
}
func (o fixedOracle) RepresentativeVectors(_ context.Context, _ []string) (map[string][]float32, error) {
	return nil, nil
}

func TestGenerateContentUsesCurrentThreadAnchorAndPublishesOneSelectionAcrossRetry(t *testing.T) {
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"active", "other"} {
		if err := db.CreateThread(&threadv1.Thread{Id: id, CreatedAt: timestamppb.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	active := &threadv1.Message{
		Id: "active-event", ThreadId: "active", Role: threadv1.Role_ROLE_USER,
		Content: pbtext.BlocksFromText("active request"), Position: 0,
	}
	foreign := &threadv1.Message{
		Id: "foreign-prerequisite", ThreadId: "other", Role: threadv1.Role_ROLE_USER,
		Content: pbtext.BlocksFromText("foreign prerequisite"), Position: 0,
	}
	if err := db.InsertMessage(active, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertMessage(foreign, nil); err != nil {
		t.Fatal(err)
	}

	cfg := rrc.DefaultConfig()
	cfg.Chunk.Estimator = testEstimator{}
	cfg.LocalContextSize = 1
	cfg.MinBatchStdDev = 0
	scorer := &fixedScorer{}
	engine := rrc.NewEngine(cfg, scorer, rrc.WithChunkOracle(fixedOracle{
		candidate: rrc.ChunkRef{MessageID: foreign.Id, ChunkIndex: 0, Text: "foreign prerequisite"},
	}))
	completer := &captureCompleter{overflow: true}
	llm := NewRRCLLM(engine, completer, db, "active", "model")
	llm.Scope = threadv1.SelectionScope_SELECTION_SCOPE_ALL_THREADS

	var selections int
	var selectedResult *rrcv1.SelectionResult
	llm.OnSelection = func(result *rrcv1.SelectionResult) {
		selections++
		selectedResult = result
	}
	var assemblies int
	llm.OnAssemble = func(rrc.AssembleTelemetry) { assemblies++ }

	req := &model.LLMRequest{
		Model: "model",
		Contents: []*genai.Content{{
			Role: "user", Parts: []*genai.Part{{Text: "active request"}},
		}},
	}
	for _, err := range llm.GenerateContent(t.Context(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
	}

	if selections != 1 {
		t.Fatalf("selection callbacks=%d, want one event for the outbound call", selections)
	}
	if assemblies != 2 || len(completer.requests) != 2 {
		t.Fatalf("overflow retry should reassemble and resend once: assemblies=%d requests=%d", assemblies, len(completer.requests))
	}
	if selectedResult == nil || selectedResult.AnchorMessageId != active.Id {
		t.Fatalf("all-thread corpus replaced active-thread anchor: %+v", selectedResult)
	}
	if scorer.calls != 1 {
		t.Fatalf("retry should hit Local Context fingerprint cache; scorer calls=%d", scorer.calls)
	}
	lastWire := completer.requests[len(completer.requests)-1].Messages
	if len(lastWire) == 0 || pbtext.TextFromBlocks(lastWire[len(lastWire)-1].Content) != "active request\n" {
		t.Fatalf("Local Context does not end in active event: %+v", lastWire)
	}
}

// TestRecordProvenance_BanksRawEvidenceOnly pins A2-W5/W6: provenance
// contributors are the turn record at 1.0 plus selected prerequisites at
// their RAW via-path evidence (provenance_weight) — never the effective
// (calibrated, mass-lifted, MMR-rewritten) score, which measurably feeds
// the lift back into the next turn's mass (raw sim 0.36 banked as 0.99,
// re-lifted every turn). Zero-evidence entries (legacy blobs predating
// the field) bank nothing — a weightless edge adds rows, not mass.
func TestRecordProvenance_BanksRawEvidenceOnly(t *testing.T) {
	cfg := rrc.DefaultConfig()
	cfg.Chunk.Estimator = testEstimator{}
	engine := rrc.NewEngine(cfg, &fixedScorer{})
	llm := NewRRCLLM(engine, nil, nil, "t1", "model")

	var banked []*rrcv1.Edge
	llm.OnEdge = func(e *rrcv1.Edge) { banked = append(banked, e) }

	anchor := &threadv1.Message{Id: "anchor", ThreadId: "t1"}
	turn := []*threadv1.Message{
		{Id: "trigger", ThreadId: "t1"},
		anchor,
	}
	sel := &rrcv1.SelectionResult{Selected: []*rrcv1.SelectedMessage{
		// The measured echo shape: raw 0.36 lifted to 0.99 effective.
		// The RAW value is what must reach the graph.
		{MessageId: "hitchhiker", ThreadId: "t1", EffectiveScore: 0.99, ProvenanceWeight: 0.36},
		{MessageId: "legacy", ThreadId: "t1", EffectiveScore: 0.90}, // provenance_weight zero
	}}
	llm.recordProvenance(anchor, turn, sel)

	weights := map[string]float32{}
	for _, e := range banked {
		if e.ToMessageId != "anchor" {
			t.Fatalf("edge target %q, want anchor", e.ToMessageId)
		}
		weights[e.FromMessageId] = e.Score
	}
	if got, ok := weights["hitchhiker"]; !ok || got != 0.36 {
		t.Fatalf("selected banked at %v (present=%v), want raw 0.36 (effective was 0.99)", got, ok)
	}
	if _, ok := weights["legacy"]; ok {
		t.Fatal("zero-evidence entry must bank nothing")
	}
	if got, ok := weights["trigger"]; !ok || got != 1.0 {
		t.Fatalf("turn-record contributor banked at %v (present=%v), want 1.0", got, ok)
	}
}

// TestGenerateContent_WindowDeliversPrecedingTurnWhole pins A2-W1: the
// wire's guaranteed window is the immediately preceding turn ∪ the
// current turn, each fetched TURN-COMPLETE. A preceding turn longer than
// LocalContextSize is delivered whole — the old recency-bounded fetch
// decapitated it (and lost it entirely once the current turn outgrew the
// limit), leaving the model blind to the exchange it was continuing.
func TestGenerateContent_WindowDeliversPrecedingTurnWhole(t *testing.T) {
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateThread(&threadv1.Thread{Id: "w", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	mk := func(id string, pos int64, turn, text string) *threadv1.Message {
		return &threadv1.Message{
			Id: id, ThreadId: "w", TurnId: turn, Role: threadv1.Role_ROLE_USER,
			Content: pbtext.BlocksFromText(text), Position: pos,
		}
	}
	for _, m := range []*threadv1.Message{
		mk("old0", 0, "turn-Z", "old zero"),
		mk("p0", 1, "turn-A", "prev step zero"),
		mk("p1", 2, "turn-A", "prev step one"),
		mk("p2", 3, "turn-A", "prev step two"),
		mk("p3", 4, "turn-A", "prev step three"),
		mk("cur", 5, "turn-B", "current question"),
	} {
		if err := db.InsertMessage(m, nil); err != nil {
			t.Fatal(err)
		}
	}

	cfg := rrc.DefaultConfig()
	cfg.Chunk.Estimator = testEstimator{}
	cfg.LocalContextSize = 2 // the recency fetch alone would hold only {p3, cur}
	engine := rrc.NewEngine(cfg, &fixedScorer{}, rrc.WithChunkOracle(fixedOracle{
		candidate: rrc.ChunkRef{MessageID: "old0", ChunkIndex: 0, Text: "old zero"},
	}))
	completer := &captureCompleter{}
	llm := NewRRCLLM(engine, completer, db, "w", "model")
	llm.CurrentTurnID = "turn-B"

	req := &model.LLMRequest{
		Model: "model",
		Contents: []*genai.Content{{
			Role: "user", Parts: []*genai.Part{{Text: "current question"}},
		}},
	}
	for _, err := range llm.GenerateContent(t.Context(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
	}

	if len(completer.requests) != 1 {
		t.Fatalf("requests=%d, want 1", len(completer.requests))
	}
	wire := completer.requests[0].Messages
	got := map[string]bool{}
	for _, m := range wire {
		got[pbtext.TextFromBlocks(m.Content)] = true
	}
	for _, want := range []string{
		"prev step zero\n", "prev step one\n", "prev step two\n", "prev step three\n", "current question\n",
	} {
		if !got[want] {
			t.Fatalf("wire missing %q — the preceding turn must be delivered whole, turn-complete; wire=%v", want, got)
		}
	}
	if pbtext.TextFromBlocks(wire[len(wire)-1].Content) != "current question\n" {
		t.Fatalf("wire must end with the current turn: %+v", wire[len(wire)-1])
	}
}
