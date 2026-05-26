package ollama

import (
	"encoding/json"
	"strings"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Options  any           `json:"options,omitempty"`
	Tools    []ollamaTool  `json:"tools,omitempty"`
}

type ollamaTool struct {
	Type     string             `json:"type"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Thinking  string           `json:"thinking,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	// Tool-response correlation (OpenAI-compatible). Required by Ollama
	// when Role=="tool" so the model can match the response to the
	// original tool call. Without this, the model sees the tool's
	// output but can't correlate it to the call it made and behaves
	// as if the tool returned null/empty.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type ollamaToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type chatResponse struct {
	Message      chatMessage `json:"message"`
	Model        string      `json:"model"`
	Done         bool        `json:"done"`
	PromptEval   int         `json:"prompt_eval_count"`
	EvalCount    int         `json:"eval_count"`
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func toLlamaMsgs(msgs []*pb.LLMMessage) []chatMessage {
	// Phase 1: extract a flat sequence of "atoms" preserving order:
	//   - text/thinking content on its role-bearing message
	//   - each tool_call with its id + name + arguments
	//   - each tool_result with its tool_call_id + content
	type atom struct {
		kind       string // "text", "toolcall", "toolresult"
		role       string // only for text atoms
		text       string
		thinking   string
		toolCall   ollamaToolCall
		toolResult *pb.ToolResultContent
	}
	var atoms []atom
	for _, m := range msgs {
		role := roleStr(m.Role)
		var textParts []string
		var thinkParts []string
		var msgCalls []ollamaToolCall
		var msgResults []*pb.ToolResultContent
		for _, b := range m.Content {
			if t := b.GetText(); t != nil {
				textParts = append(textParts, t.Text)
			}
			if t := b.GetThinking(); t != nil {
				thinkParts = append(thinkParts, t.Text)
			}
			if tc := b.GetToolCall(); tc != nil {
				msgCalls = append(msgCalls, ollamaToolCall{
					ID:   tc.Id,
					Type: "function",
					Function: struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					}{
						Name:      tc.Name,
						Arguments: json.RawMessage(tc.Arguments),
					},
				})
			}
			if tr := b.GetToolResult(); tr != nil {
				msgResults = append(msgResults, tr)
			}
		}
		// Emit atoms for this source message. Text/thinking first, then
		// tool_calls, then tool_results — matches the intrinsic order
		// inside a multi-block assistant message (thinking precedes
		// tool invocation precedes response in the execution timeline).
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

	// Phase 2a: build the set of tool_call IDs that have a matching
	// tool_result atom somewhere in the input. Tool calls without a
	// matching response are orphans — the model emitted the call in
	// a prior turn but the response either wasn't picked by Selection
	// or was dropped by shed. Sending orphan tool_calls to the
	// provider trips protocol validation (assistant.tool_calls must
	// be followed by tool messages matching every id). Same direction
	// as orphan tool_results: drop the side that can't be satisfied.
	resultIDs := make(map[string]bool)
	for _, a := range atoms {
		if a.kind == "toolresult" && a.toolResult != nil {
			resultIDs[a.toolResult.ToolCallId] = true
		}
	}

	// Phase 2b: walk atoms and emit canonical chatMessages.
	//
	// Rules enforced here:
	//   - Only tool_calls whose id is in resultIDs are kept — the
	//     rest are orphans, dropped.
	//   - Consecutive kept tool_calls are merged into one assistant
	//     with a tool_calls array.
	//   - Matching tool_results are emitted immediately after their
	//     paired assistant-with-tool_calls, in input order, then
	//     marked consumed.
	//   - A thinking-only atom (no text, just reasoning) does NOT
	//     emit its own chatMessage — standalone
	//     role=assistant/content=""/thinking-only messages violate
	//     the OpenAI tool-use protocol (assistant must have content
	//     or tool_calls). Instead the thinking is buffered and
	//     merged into the NEXT emitted assistant message that has
	//     text or tool_calls, preserving the reason-before-action
	//     chronology. Trailing thinking with no subsequent action
	//     is dropped.
	//   - Text atoms with non-empty text emit as regular messages,
	//     absorbing any buffered thinking.
	//   - Orphan tool_results (no matching call in the kept output)
	//     drop.
	out := make([]chatMessage, 0, len(atoms))
	consumed := make([]bool, len(atoms))
	var pendingThinking []string // accumulated from thinking-only atoms
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
				// Thinking only — buffer for merge into next actionable
				// message. Do not emit on its own.
				pendingThinking = append(pendingThinking, a.thinking)
				consumed[i] = true
				continue
			}
			// Text (with or without thinking) — emit, absorbing any
			// prior buffered thinking as the leading reasoning.
			thinking := a.thinking
			if buf := flushThinking(); buf != "" {
				if thinking != "" {
					thinking = buf + "\n" + thinking
				} else {
					thinking = buf
				}
			}
			out = append(out, chatMessage{
				Role:     a.role,
				Content:  a.text,
				Thinking: thinking,
			})
			consumed[i] = true
		case "toolcall":
			// Greedy-collect consecutive toolcalls, KEEPING ONLY
			// those with a matching tool_result somewhere in the
			// input. If no call in the group has a match, drop the
			// whole group — we won't emit an assistant with all
			// orphan tool_calls.
			var group []ollamaToolCall
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
			out = append(out, chatMessage{
				Role:      "assistant",
				Thinking:  flushThinking(),
				ToolCalls: group,
			})
			// Emit matching tool responses from the remaining atoms.
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
				out = append(out, chatMessage{
					Role:       "tool",
					ToolCallID: tr.ToolCallId,
					Content:    tr.Content,
				})
				consumed[k] = true
			}
		case "toolresult":
			// Orphan tool_result — no preceding tool_call remains to
			// bind to. Drop. Any buffered thinking here is stranded
			// too; it can't attach to a toolresult meaningfully, so
			// drop it as well on the next flush.
			consumed[i] = true
		}
	}
	// Trailing thinking with no subsequent action — drop, since
	// emitting a thinking-only assistant is exactly the protocol
	// violation this function exists to prevent.
	flushThinking()
	return out
}

func roleStr(r pb.Role) string {
	switch r {
	case pb.Role_ROLE_USER:
		return "user"
	case pb.Role_ROLE_ASSISTANT:
		return "assistant"
	case pb.Role_ROLE_SYSTEM:
		return "system"
	default:
		return "user"
	}
}

func providerOpts(req *pb.CompletionRequest) map[string]any {
	opts := make(map[string]any)
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		opts["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		opts["stop"] = req.Stop
	}
	if len(opts) == 0 {
		return nil
	}
	return opts
}

func toOllamaTools(tools []*pb.ToolDeclaration) []ollamaTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]ollamaTool, len(tools))
	for i, t := range tools {
		params := json.RawMessage(t.ParametersJson)
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		out[i] = ollamaTool{
			Type: "function",
			Function: ollamaToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		}
	}
	return out
}

func ptr(s string) *string { return &s }
