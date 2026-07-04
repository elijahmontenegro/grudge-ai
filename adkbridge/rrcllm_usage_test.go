package adkbridge

import (
	"context"
	"errors"
	"iter"
	"sync/atomic"
	"testing"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/proto/pbtext"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/service/storage"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeScales records observations and serves a fixed scale.
type fakeScales struct {
	scale    float64
	observed [][3]int // {predicted, observed, budget}
}

func (f *fakeScales) Scale() float64 { return f.scale }
func (f *fakeScales) Observe(predicted, observed, budget int) error {
	f.observed = append(f.observed, [3]int{predicted, observed, budget})
	return nil
}

// usageCompleter answers Complete with a fixed usage; optionally the
// first call fails with a context overflow.
type usageCompleter struct {
	usage         *llmv1.Usage
	overflowFirst bool
	requests      []*llmv1.CompletionRequest
}

func (c *usageCompleter) Complete(_ context.Context, req *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error) {
	c.requests = append(c.requests, req)
	if c.overflowFirst {
		c.overflowFirst = false
		return nil, errors.New("context length exceeded")
	}
	return &llmv1.CompletionResponse{
		Message: &llmv1.LLMMessage{Role: threadv1.Role_ROLE_ASSISTANT, Content: pbtext.BlocksFromText("ok")},
		Usage:   c.usage,
	}, nil
}

func (c *usageCompleter) Stream(context.Context, *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error] {
	return func(func(*llmv1.StreamChunk, error) bool) {}
}

// chunkStreamer streams a fixed chunk sequence; a non-nil finalErr is
// yielded as an iterator error after the chunks.
type chunkStreamer struct {
	chunks   []*llmv1.StreamChunk
	finalErr error
}

func (c *chunkStreamer) Complete(context.Context, *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error) {
	return nil, errors.New("not used")
}

func (c *chunkStreamer) Stream(context.Context, *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error] {
	return func(yield func(*llmv1.StreamChunk, error) bool) {
		for _, ch := range c.chunks {
			if !yield(ch, nil) {
				return
			}
		}
		if c.finalErr != nil {
			yield(nil, c.finalErr)
		}
	}
}

// usageHarness builds a single-thread corpus with one stored user
// message and an RRCLLM over the given completer.
func usageHarness(t *testing.T, completer interface {
	Complete(context.Context, *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error)
	Stream(context.Context, *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error]
}) *RRCLLM {
	t.Helper()
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateThread(&threadv1.Thread{Id: "t", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	// m0 is a selectable prerequisite — the overflow-retry path needs
	// a delivery group it can shed. m1 is the anchor.
	if err := db.InsertMessage(&threadv1.Message{
		Id: "m0", ThreadId: "t", Role: threadv1.Role_ROLE_USER,
		Content: pbtext.BlocksFromText("prerequisite content"), Position: 0,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertMessage(&threadv1.Message{
		Id: "m1", ThreadId: "t", Role: threadv1.Role_ROLE_USER,
		Content: pbtext.BlocksFromText("the request"), Position: 1,
	}, nil); err != nil {
		t.Fatal(err)
	}

	cfg := rrc.DefaultConfig()
	cfg.Chunk.Estimator = testEstimator{}
	cfg.LocalContextSize = 1
	cfg.MinBatchStdDev = 0
	cfg.DiversityLambda = 0
	cfg.ContextBudgetTokens = 1000
	cfg.BudgetHeadroomPct = 0.9
	engine := rrc.NewEngine(cfg, &fixedScorer{}, rrc.WithChunkOracle(fixedOracle{
		candidate: rrc.ChunkRef{MessageID: "m0", ChunkIndex: 0, Text: "prerequisite content"},
	}))
	return NewRRCLLM(engine, completer, db, "t", "test-model")
}

func adkReq() *model.LLMRequest {
	return &model.LLMRequest{
		Model: "test-model",
		Contents: []*genai.Content{{
			Role: "user", Parts: []*genai.Part{{Text: "the request"}},
		}},
	}
}

func drain(t *testing.T, llm *RRCLLM, stream bool) {
	t.Helper()
	for _, err := range llm.GenerateContent(t.Context(), adkReq(), stream) {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// A successful completion feeds the learner with the assembly's own
// prediction, the reported prompt tokens, and the budget the assembly
// ran under — and OnUsage sees the same pair atomically.
func TestGenerateContent_ObservesUsageWithMatchingPrediction(t *testing.T) {
	llm := usageHarness(t, &usageCompleter{usage: &llmv1.Usage{PromptTokens: 480, CompletionTokens: 7}})
	scales := &fakeScales{}
	llm.Scales = scales

	var predicted int
	llm.OnAssemble = func(tt rrc.AssembleTelemetry) { predicted = tt.TotalTokens }
	var pairs [][2]int
	llm.OnUsage = func(p int, u *llmv1.Usage) { pairs = append(pairs, [2]int{p, int(u.PromptTokens)}) }

	drain(t, llm, false)

	if predicted <= 0 {
		t.Fatalf("no assembly telemetry captured")
	}
	if len(scales.observed) != 1 {
		t.Fatalf("Observe calls = %d, want 1", len(scales.observed))
	}
	if got := scales.observed[0]; got != [3]int{predicted, 480, 1000} {
		t.Fatalf("Observe(predicted, observed, budget) = %v, want [%d 480 1000]", got, predicted)
	}
	if len(pairs) != 1 || pairs[0] != [2]int{predicted, 480} {
		t.Fatalf("OnUsage pairs = %v, want one (%d, 480)", pairs, predicted)
	}
}

// A learned scale converts the model-truth budget into counter units
// before assembly; the declared window on the wire stays model truth.
func TestGenerateContent_ScaleConvertsBudgetNotWindow(t *testing.T) {
	completer := &usageCompleter{usage: &llmv1.Usage{PromptTokens: 480, CompletionTokens: 7}}
	llm := usageHarness(t, completer)
	scales := &fakeScales{scale: 2.0}
	llm.Scales = scales

	var effective int
	llm.OnAssemble = func(tt rrc.AssembleTelemetry) { effective = tt.EffectiveBudget }

	drain(t, llm, false)

	// 1000 model-truth tokens / scale 2.0 = 500 counter units; the
	// 0.9 headroom applies on top.
	if effective != 450 {
		t.Fatalf("EffectiveBudget = %d, want 450 (1000/2.0 × 0.9)", effective)
	}
	if len(scales.observed) != 1 || scales.observed[0][2] != 500 {
		t.Fatalf("Observe budget = %v, want converted 500", scales.observed)
	}
	if len(completer.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(completer.requests))
	}
	if w := completer.requests[0].GetContextWindowTokens(); w != 1000 {
		t.Fatalf("ContextWindowTokens on the wire = %d, want unconverted 1000", w)
	}
}

// Overflow retries carry no usage (the failed call returned an
// error); only the successful final attempt is observed, against its
// own re-assembled prediction.
func TestGenerateContent_OverflowRetryObservesOnlySuccess(t *testing.T) {
	llm := usageHarness(t, &usageCompleter{
		usage:         &llmv1.Usage{PromptTokens: 480, CompletionTokens: 7},
		overflowFirst: true,
	})
	scales := &fakeScales{}
	llm.Scales = scales

	var totals []int
	llm.OnAssemble = func(tt rrc.AssembleTelemetry) { totals = append(totals, tt.TotalTokens) }

	drain(t, llm, false)

	if len(totals) != 2 {
		t.Fatalf("assemblies = %d, want 2 (initial + overflow retry)", len(totals))
	}
	if len(scales.observed) != 1 {
		t.Fatalf("Observe calls = %d, want 1 (success only)", len(scales.observed))
	}
	if scales.observed[0][0] != totals[len(totals)-1] {
		t.Fatalf("observed prediction %d is not the final attempt's %d", scales.observed[0][0], totals[len(totals)-1])
	}
}

// The four no-usage stream exits: provider error chunk, stream end
// without a Done chunk, mid-stream iterator error, and consumer
// abort. None may observe, none may panic.
func TestGenerateContent_NoUsageStreamExitsDoNotObserve(t *testing.T) {
	usage := &llmv1.Usage{PromptTokens: 480, CompletionTokens: 7}
	text := &llmv1.StreamChunk{Delta: &llmv1.StreamChunk_Text{Text: &threadv1.TextContent{Text: "hi"}}}
	boom := "boom"

	cases := []struct {
		name     string
		streamer *chunkStreamer
		abort    bool
	}{
		{"provider error chunk", &chunkStreamer{chunks: []*llmv1.StreamChunk{
			text, {Done: true, Error: &boom, Usage: usage},
		}}, false},
		{"no done chunk", &chunkStreamer{chunks: []*llmv1.StreamChunk{text}}, false},
		{"mid-stream error yields error response", &chunkStreamer{
			chunks: []*llmv1.StreamChunk{text}, finalErr: errors.New("conn reset"),
		}, false},
		{"consumer abort", &chunkStreamer{chunks: []*llmv1.StreamChunk{
			text, {Done: true, Usage: usage},
		}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm := usageHarness(t, tc.streamer)
			scales := &fakeScales{}
			llm.Scales = scales
			var usages int
			llm.OnUsage = func(int, *llmv1.Usage) { usages++ }

			for _, err := range llm.GenerateContent(t.Context(), adkReq(), true) {
				if err != nil {
					t.Fatal(err)
				}
				if tc.abort {
					break
				}
			}

			if len(scales.observed) != 0 {
				t.Fatalf("Observe called on a no-usage exit: %v", scales.observed)
			}
			if usages != 0 {
				t.Fatalf("OnUsage fired on a no-usage exit")
			}
		})
	}
}

// The happy streaming path observes the terminal chunk's usage.
func TestGenerateContent_StreamUsageObserved(t *testing.T) {
	llm := usageHarness(t, &chunkStreamer{chunks: []*llmv1.StreamChunk{
		{Delta: &llmv1.StreamChunk_Text{Text: &threadv1.TextContent{Text: "hi"}}},
		{Done: true, Usage: &llmv1.Usage{PromptTokens: 480, CompletionTokens: 7}},
	}})
	scales := &fakeScales{}
	llm.Scales = scales

	drain(t, llm, true)

	if len(scales.observed) != 1 || scales.observed[0][1] != 480 {
		t.Fatalf("Observe = %v, want one call with observed 480", scales.observed)
	}
}

// The injected counting projection reaches wire sizing.
func TestGenerateContent_CountTextIsUsed(t *testing.T) {
	llm := usageHarness(t, &usageCompleter{})
	var calls atomic.Int64
	llm.CountText = func(m *llmv1.LLMMessage) string {
		calls.Add(1)
		return pbtext.TextFromBlocks(m.Content)
	}

	drain(t, llm, false)

	if calls.Load() == 0 {
		t.Fatal("CountText never called during assembly")
	}
}
