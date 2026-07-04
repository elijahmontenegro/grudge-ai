package rrc

import (
	"context"
	"strings"
	"testing"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// TestAssemble_CountTextProjection — the caller-injected counting
// projection replaces TextFromBlocks in wire sizing, so content a
// provider's codec never sends (e.g. thinking blocks) stops counting
// against the budget. Nil CountText keeps the count-everything
// default.
func TestAssemble_CountTextProjection(t *testing.T) {
	anchor := &threadv1.Message{
		Id:       "q",
		ThreadId: "t1",
		Role:     threadv1.Role_ROLE_USER,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{
				Text: "0123456789012345678901234567890123456789", // 40 chars
			}}},
			{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{
				Text: string(make([]byte, 400)),
			}}},
		},
	}

	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}

	run := func(countText func(*llmv1.LLMMessage) string) AssembleResult {
		t.Helper()
		e := NewEngine(cfg, newMockScorer())
		res, err := e.Assemble(context.Background(), AssembleRequest{
			Anchor:       anchor,
			Corpus:       []*threadv1.Message{anchor},
			LocalContext: []*threadv1.Message{anchor},
			Scope:        threadv1.SelectionScope_SELECTION_SCOPE_THREAD,
			ThreadID:     "t1",
			// Reuse path: no prerequisite selection, no oracle — the
			// test isolates wire sizing.
			PriorSelection: &rrcv1.SelectionResult{EventId: "sel-q", AnchorMessageId: "q"},
			CountText:      countText,
		})
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		return res
	}

	textOnly := func(m *llmv1.LLMMessage) string {
		var sb strings.Builder
		for _, b := range m.Content {
			if tb := b.GetText(); tb != nil {
				sb.WriteString(tb.Text)
			}
		}
		return sb.String()
	}

	full := run(nil).Telemetry.TotalTokens
	projected := run(textOnly).Telemetry.TotalTokens

	// charEstimator: text-only = (40+3)/4 = 10; the default counts
	// text + thinking (+ newlines) = (442+3)/4 = 111.
	if projected != 10 {
		t.Fatalf("projected TotalTokens = %d, want 10", projected)
	}
	if full != 111 {
		t.Fatalf("default TotalTokens = %d, want 111", full)
	}
}
