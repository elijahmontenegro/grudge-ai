package ollama

import (
	"encoding/json"

	"github.com/elijahmontenegro/grudge/core/adapter/internal/chatwire"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
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
	Message    chatMessage `json:"message"`
	Model      string      `json:"model"`
	Done       bool        `json:"done"`
	PromptEval int         `json:"prompt_eval_count"`
	EvalCount  int         `json:"eval_count"`
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// toLlamaMsgs converts a corpus wire slice into ollama's chat message
// shape. The pairing/merging/thinking-buffering algorithm lives in
// chatwire (shared with the openai adapter, which needs the identical
// tool_calls/role:"tool" canonicalization minus the Thinking field);
// this is purely the per-message type conversion.
func toLlamaMsgs(msgs []*llmv1.LLMMessage) []chatMessage {
	canon := chatwire.Canonicalize(msgs)
	out := make([]chatMessage, len(canon))
	for i, m := range canon {
		cm := chatMessage{
			Role:       m.Role,
			Content:    m.Text,
			Thinking:   m.Thinking,
			ToolCallID: m.ToolCallID,
		}
		if len(m.ToolCalls) > 0 {
			cm.ToolCalls = make([]ollamaToolCall, len(m.ToolCalls))
			for j, c := range m.ToolCalls {
				cm.ToolCalls[j] = ollamaToolCall{ID: c.ID, Type: "function"}
				cm.ToolCalls[j].Function.Name = c.Name
				cm.ToolCalls[j].Function.Arguments = json.RawMessage(c.Arguments)
			}
		}
		out[i] = cm
	}
	return out
}

func providerOpts(req *llmv1.CompletionRequest) map[string]any {
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
	// Without num_ctx ollama serves the model's default context
	// (often 4096) and SILENTLY truncates the prompt head past it —
	// HTTP 200, no error, prompt_eval_count capped at the truncation.
	// The caller's declared window is the only defense.
	if req.ContextWindowTokens != nil && *req.ContextWindowTokens > 0 {
		opts["num_ctx"] = *req.ContextWindowTokens
	}
	if len(opts) == 0 {
		return nil
	}
	return opts
}

func toOllamaTools(tools []*llmv1.ToolDeclaration) []ollamaTool {
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
