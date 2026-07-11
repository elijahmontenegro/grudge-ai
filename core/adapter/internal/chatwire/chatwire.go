// Package chatwire canonicalizes grudge's corpus wire messages into the
// OpenAI-compatible chat shape shared by ollama and openai: assistant
// messages carrying a tool_calls array, and role="tool" messages
// keyed by tool_call_id. The pairing/merging/ordering algorithm is
// provider-agnostic and lives only here; each adapter converts the
// canonical Message 1:1 to its own wire struct.
package chatwire

import (
	"strings"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// Message is one canonical OpenAI-compatible chat turn.
type Message struct {
	Role       string     // "user" | "assistant" | "system" | "tool"
	Text       string     // "" for a tool_call-only assistant or a role=tool carrier
	Thinking   string     // merged reasoning text; adapters that never send thinking (openai) discard it
	ToolCalls  []ToolCall // assistant only
	ToolCallID string     // role=="tool" only
}

// ToolCall is one canonical tool invocation. Arguments is always a
// non-empty JSON object string ("{}" when the source had none) so an
// adapter can wrap it directly as a raw JSON value without a nil/empty
// marshal hazard.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// RoleString maps a proto Role to its OpenAI-compatible wire string.
// Unspecified/unknown roles default to "user".
func RoleString(r threadv1.Role) string {
	switch r {
	case threadv1.Role_ROLE_USER:
		return "user"
	case threadv1.Role_ROLE_ASSISTANT:
		return "assistant"
	case threadv1.Role_ROLE_SYSTEM:
		return "system"
	default:
		return "user"
	}
}

// Canonicalize converts a corpus wire slice into canonical
// OpenAI-compatible chat messages.
//
// Phase 1 extracts a flat ordered "atom" sequence (text/thinking,
// tool_call, tool_result) — text/thinking first, then tool_calls, then
// tool_results, matching the intrinsic order inside a multi-block
// assistant message (thinking precedes invocation precedes response
// in the execution timeline).
//
// Phase 2 walks the atoms and emits canonical messages, applying:
//   - Orphan drop: a tool_call with no matching tool_result anywhere
//     in the input (Selection or shed can strand either side) is
//     dropped — sending it trips protocol validation (every
//     assistant.tool_calls id must have a matching tool response).
//     Consecutive kept tool_calls merge into one assistant message; if
//     a whole consecutive group is orphaned, the group drops. Orphan
//     tool_results (no surviving call to bind to) drop.
//   - Thinking buffering: a thinking-only atom does NOT emit its own
//     message — a standalone assistant with no content and no
//     tool_calls violates the tool-use protocol. It buffers instead
//     and merges into the next actionable (text or tool_calls)
//     message, preserving reason-before-action chronology. Trailing
//     thinking with no subsequent action drops.
func Canonicalize(msgs []*llmv1.LLMMessage) []Message {
	type atom struct {
		kind       string // "text", "toolcall", "toolresult"
		role       string // only for text atoms
		text       string
		thinking   string
		toolCall   ToolCall
		toolResult *threadv1.ToolResultContent
	}
	var atoms []atom
	for _, m := range msgs {
		role := RoleString(m.Role)
		var textParts []string
		var thinkParts []string
		var msgCalls []ToolCall
		var msgResults []*threadv1.ToolResultContent
		for _, b := range m.Content {
			if t := b.GetText(); t != nil {
				textParts = append(textParts, t.Text)
			}
			if t := b.GetThinking(); t != nil {
				thinkParts = append(thinkParts, t.Text)
			}
			if tc := b.GetToolCall(); tc != nil {
				args := tc.Arguments
				if args == "" {
					args = "{}"
				}
				msgCalls = append(msgCalls, ToolCall{ID: tc.Id, Name: tc.Name, Arguments: args})
			}
			if tr := b.GetToolResult(); tr != nil {
				msgResults = append(msgResults, tr)
			}
		}
		if len(textParts) > 0 || len(thinkParts) > 0 {
			atoms = append(atoms, atom{
				kind:     "text",
				role:     role,
				text:     strings.Join(textParts, "\n"),
				thinking: strings.Join(thinkParts, "\n"),
			})
		}
		for _, tc := range msgCalls {
			atoms = append(atoms, atom{kind: "toolcall", toolCall: tc})
		}
		for _, tr := range msgResults {
			atoms = append(atoms, atom{kind: "toolresult", toolResult: tr})
		}
	}

	resultIDs := make(map[string]bool)
	for _, a := range atoms {
		if a.kind == "toolresult" && a.toolResult != nil {
			resultIDs[a.toolResult.ToolCallId] = true
		}
	}

	out := make([]Message, 0, len(atoms))
	consumed := make([]bool, len(atoms))
	var pendingThinking []string
	flushThinking := func() string {
		if len(pendingThinking) == 0 {
			return ""
		}
		t := strings.Join(pendingThinking, "\n")
		pendingThinking = pendingThinking[:0]
		return t
	}
	for i := 0; i < len(atoms); i++ {
		if consumed[i] {
			continue
		}
		a := atoms[i]
		switch a.kind {
		case "text":
			if a.text == "" && a.thinking == "" {
				// No content at all — drop silently.
				consumed[i] = true
				continue
			}
			if a.text == "" && a.thinking != "" {
				pendingThinking = append(pendingThinking, a.thinking)
				consumed[i] = true
				continue
			}
			thinking := a.thinking
			if buf := flushThinking(); buf != "" {
				if thinking != "" {
					thinking = buf + "\n" + thinking
				} else {
					thinking = buf
				}
			}
			out = append(out, Message{
				Role:     a.role,
				Text:     a.text,
				Thinking: thinking,
			})
			consumed[i] = true
		case "toolcall":
			// Greedy-collect consecutive toolcalls, keeping only those
			// with a matching tool_result somewhere in the input.
			var group []ToolCall
			j := i
			for j < len(atoms) && atoms[j].kind == "toolcall" && !consumed[j] {
				consumed[j] = true
				if resultIDs[atoms[j].toolCall.ID] {
					group = append(group, atoms[j].toolCall)
				}
				j++
			}
			if len(group) == 0 {
				// Orphan toolcall group — pending thinking loses its
				// intended action and is dropped too.
				flushThinking()
				continue
			}
			callIDs := make(map[string]bool, len(group))
			for _, c := range group {
				callIDs[c.ID] = true
			}
			out = append(out, Message{
				Role:      "assistant",
				Thinking:  flushThinking(),
				ToolCalls: group,
			})
			for k := j; k < len(atoms); k++ {
				if consumed[k] {
					continue
				}
				if atoms[k].kind != "toolresult" {
					continue
				}
				tr := atoms[k].toolResult
				if !callIDs[tr.ToolCallId] {
					continue
				}
				out = append(out, Message{
					Role:       "tool",
					ToolCallID: tr.ToolCallId,
					Text:       tr.Content,
				})
				consumed[k] = true
			}
		case "toolresult":
			// Orphan tool_result — no preceding tool_call remains to
			// bind to. Drop; any buffered thinking here is stranded
			// too and drops on the next flush.
			consumed[i] = true
		}
	}
	// Trailing thinking with no subsequent action — drop, since
	// emitting a thinking-only assistant is exactly the protocol
	// violation this function exists to prevent.
	flushThinking()
	return out
}
