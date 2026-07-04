package openai

import (
	"strings"

	"github.com/elijahmontenegro/grudge/core"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
)

func init() { core.RegisterCountProjection("openai", CountText) }

// CountText is this adapter's counting projection: the text one wire
// message actually contributes to an OpenAI-compat request. Mirrors
// toChatMessage/toMultipart — text blocks only; thinking, tool-call,
// tool-result, and attachment blocks are never sent, so they must not
// count against the budget.
func CountText(m *llmv1.LLMMessage) string {
	var sb strings.Builder
	for _, b := range m.Content {
		if t := b.GetText(); t != nil {
			sb.WriteString(t.Text)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
