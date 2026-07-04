package genaikit

import (
	"strings"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// CountText is the counting projection shared by every genai-backed
// adapter: the text one wire message actually contributes to a
// request. Mirrors genaicodec.ProtoToContent — text, thinking (sent
// as Thought parts), tool calls (name + args), tool results;
// attachment blocks are never sent, so they must not count against
// the budget.
func CountText(m *llmv1.LLMMessage) string {
	var sb strings.Builder
	for _, b := range m.Content {
		switch v := b.Block.(type) {
		case *threadv1.ContentBlock_Text:
			sb.WriteString(v.Text.Text)
			sb.WriteByte('\n')
		case *threadv1.ContentBlock_Thinking:
			sb.WriteString(v.Thinking.Text)
			sb.WriteByte('\n')
		case *threadv1.ContentBlock_ToolCall:
			sb.WriteString(v.ToolCall.Name)
			sb.WriteString(": ")
			sb.WriteString(v.ToolCall.Arguments)
			sb.WriteByte('\n')
		case *threadv1.ContentBlock_ToolResult:
			sb.WriteString(v.ToolResult.Content)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
