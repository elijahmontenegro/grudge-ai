package openai

import (
	"strings"

	"github.com/elijahmontenegro/grudge/core"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

func init() { core.RegisterCountProjection("openai", CountText) }

// CountText is this adapter's counting projection: the text one wire
// message actually contributes to an OpenAI-compat request. Mirrors
// fromCanonicalMessage — text, tool-call name+args, and tool-result
// content are all sent; thinking and attachment blocks are never
// sent (chatwire drops Thinking for this adapter), so they must not
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
