package googleai

import (
	"strings"
	"testing"

	"github.com/elijahmontenegro/grudge/core/adapter/internal/genaikit"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// The registered projection is the shared genaikit one — this adapter
// no longer has its own hand-rolled REST codec, so the count now
// includes everything the genai SDK actually sends (text, thinking,
// tool calls, tool results); only attachments are excluded.
func TestCountText_MirrorsGenaiCodec(t *testing.T) {
	m := &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "answer"}}},
			{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "REASONING"}}},
			{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{Id: "c1", Name: "Bash", Arguments: "ARGS"}}},
			{Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{ToolCallId: "c1", Content: "RESULT"}}},
			{Block: &threadv1.ContentBlock_Attachment{Attachment: &threadv1.AttachmentContent{
				Filename: "SECRET.pdf", Path: "/x", InlinedText: "INLINED",
			}}},
		},
	}
	got := genaikit.CountText(m)
	for _, want := range []string{"answer", "REASONING", "Bash", "ARGS", "RESULT"} {
		if !strings.Contains(got, want) {
			t.Fatalf("CountText missing %q: %q", want, got)
		}
	}
	for _, banned := range []string{"SECRET.pdf", "INLINED"} {
		if strings.Contains(got, banned) {
			t.Fatalf("CountText includes attachment content %q: %q", banned, got)
		}
	}
}
