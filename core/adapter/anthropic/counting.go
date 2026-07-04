package anthropic

import (
	"strings"

	"github.com/elijahmontenegro/grudge/core"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

func init() { core.RegisterCountProjection("anthropic", CountText) }

// CountText is this adapter's counting projection: the text one wire
// message actually contributes to an Anthropic request. Mirrors
// toAPIContent — text, tool_use (name + args), tool_result content;
// thinking and attachment blocks are never sent, so they must not
// count against the budget.
func CountText(m *llmv1.LLMMessage) string {
	var sb strings.Builder
	for _, b := range m.Content {
		switch v := b.Block.(type) {
		case *threadv1.ContentBlock_Text:
			sb.WriteString(v.Text.Text)
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
