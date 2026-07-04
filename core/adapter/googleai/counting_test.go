package googleai

import (
	"strings"
	"testing"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// CountText mirrors toGenerateRequest: text blocks only.
func TestCountText_TextBlocksOnly(t *testing.T) {
	m := &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "answer"}}},
			{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "SECRET"}}},
			{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{Id: "c1", Name: "Bash", Arguments: "ARGS"}}},
		},
	}
	got := CountText(m)
	if !strings.Contains(got, "answer") {
		t.Fatalf("CountText missing text block: %q", got)
	}
	for _, banned := range []string{"SECRET", "ARGS"} {
		if strings.Contains(got, banned) {
			t.Fatalf("CountText includes %q, which this codec never sends: %q", banned, got)
		}
	}
}
