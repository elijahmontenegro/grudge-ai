package vertex

import (
	"strings"
	"testing"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// CountText mirrors genaicodec.ProtoToContent: text, thinking (sent
// as Thought parts), tool calls, and tool results all reach the wire;
// attachments never do.
func TestCountText_MirrorsGenaiCodec(t *testing.T) {
	m := &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "answer"}}},
			{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "REASONING"}}},
			{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{Id: "c1", Name: "Bash", Arguments: `{"cmd":"ls"}`}}},
			{Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{ToolCallId: "c1", Content: "a.txt"}}},
			{Block: &threadv1.ContentBlock_Attachment{Attachment: &threadv1.AttachmentContent{
				Filename: "SECRET.pdf", Path: "/x", InlinedText: "INLINED",
			}}},
		},
	}
	got := CountText(m)
	for _, want := range []string{"answer", "REASONING", "Bash", "a.txt"} {
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
