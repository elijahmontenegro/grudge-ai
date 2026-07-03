package adk

import (
	"context"
	"errors"
	"iter"
	"testing"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/service/storage"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type captureCompleter struct {
	requests []*pb.CompletionRequest
	overflow bool
}

func (c *captureCompleter) Complete(_ context.Context, req *pb.CompletionRequest) (*pb.CompletionResponse, error) {
	c.requests = append(c.requests, req)
	if c.overflow {
		c.overflow = false
		return nil, errors.New("context length exceeded")
	}
	return &pb.CompletionResponse{
		Message: &pb.LLMMessage{Role: pb.Role_ROLE_ASSISTANT, Content: rrc.BlocksFromText("ok")},
	}, nil
}

func (c *captureCompleter) Stream(context.Context, *pb.CompletionRequest) iter.Seq2[*pb.StreamChunk, error] {
	return func(func(*pb.StreamChunk, error) bool) {}
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
func (o fixedOracle) DiversityRerank(_ context.Context, selected []*pb.SelectedMessage, _ map[string]float64, _ float64) ([]*pb.SelectedMessage, error) {
	return selected, nil
}

func TestGenerateContentUsesCurrentThreadAnchorAndPublishesOneSelectionAcrossRetry(t *testing.T) {
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"active", "other"} {
		if err := db.CreateThread(&pb.Thread{Id: id, CreatedAt: timestamppb.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	active := &pb.Message{
		Id: "active-event", ThreadId: "active", Role: pb.Role_ROLE_USER,
		Content: rrc.BlocksFromText("active request"), Position: 0,
	}
	foreign := &pb.Message{
		Id: "foreign-prerequisite", ThreadId: "other", Role: pb.Role_ROLE_USER,
		Content: rrc.BlocksFromText("foreign prerequisite"), Position: 0,
	}
	if err := db.InsertMessage(active, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertMessage(foreign, nil); err != nil {
		t.Fatal(err)
	}

	cfg := rrc.DefaultConfig()
	cfg.LocalContextSize = 1
	cfg.MinBatchStdDev = 0
	cfg.DiversityLambda = 0
	scorer := &fixedScorer{}
	engine := rrc.NewEngine(cfg, scorer, rrc.WithChunkOracle(fixedOracle{
		candidate: rrc.ChunkRef{MessageID: foreign.Id, ChunkIndex: 0, Text: "foreign prerequisite"},
	}))
	completer := &captureCompleter{overflow: true}
	llm := NewRRCLLM(engine, completer, db, "active", "model")
	llm.Scope = pb.SelectionScope_SELECTION_SCOPE_ALL_THREADS

	var selections int
	var selectedResult *pb.SelectionResult
	llm.OnSelection = func(result *pb.SelectionResult) {
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
	if len(lastWire) == 0 || rrc.TextFromBlocks(lastWire[len(lastWire)-1].Content) != "active request\n" {
		t.Fatalf("Local Context does not end in active event: %+v", lastWire)
	}
}
