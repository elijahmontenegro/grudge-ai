// Package genaicodec maps between the google.golang.org/genai SDK content
// types and grudge's proto content model. It is shared by every adapter that
// speaks the genai SDK (the Vertex adapter) and by the ADK bridge
// (adkbridge), so the Content/Part <-> llmv1.LLMMessage translation lives in
// exactly one place rather than being copied per consumer.
//
// It is public (not core/internal) because adkbridge sits outside core's
// internal boundary and must import it. It depends only on the generated
// proto types and the genai SDK — no service, storage, or engine
// dependency — so it is unit-testable in isolation.
package genaicodec

import (
	"encoding/json"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"google.golang.org/genai"
)

// ContentToProto converts a single genai.Content to a proto LLMMessage.
func ContentToProto(c *genai.Content) *llmv1.LLMMessage {
	msg := &llmv1.LLMMessage{
		Role: RoleToProto(c.Role),
	}
	for _, p := range c.Parts {
		if p.Text != "" {
			if p.Thought {
				msg.Content = append(msg.Content, &threadv1.ContentBlock{
					Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: p.Text}},
				})
			} else {
				msg.Content = append(msg.Content, &threadv1.ContentBlock{
					Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: p.Text}},
				})
			}
		}
		if p.FunctionCall != nil {
			argsJSON := "{}"
			if p.FunctionCall.Args != nil {
				if b, err := json.Marshal(p.FunctionCall.Args); err == nil {
					argsJSON = string(b)
				}
			}
			msg.Content = append(msg.Content, &threadv1.ContentBlock{
				Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{
					Id:        p.FunctionCall.ID,
					Name:      p.FunctionCall.Name,
					Arguments: argsJSON,
				}},
			})
		}
		if p.FunctionResponse != nil {
			// Store the FULL response map as JSON. Older code extracted
			// only the `"result"` key, which worked for tools that
			// wrapped their return as `{result: "..."}` but silently
			// dropped anything else — AskUserQuestion returns
			// `{answers: {...}}`, so the content landed in storage as
			// an empty string and the model saw null on the next
			// round (then re-asked the question as prose). Preserving
			// the whole JSON object keeps every tool's schema intact
			// regardless of key name.
			respText := ""
			if resp := p.FunctionResponse.Response; resp != nil {
				if b, err := json.Marshal(resp); err == nil {
					respText = string(b)
				}
			}
			msg.Content = append(msg.Content, &threadv1.ContentBlock{
				Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{
					ToolCallId: p.FunctionResponse.ID,
					Content:    respText,
				}},
			})
		}
	}
	return msg
}

// ProtoToContent converts a proto LLMMessage to a genai.Content.
func ProtoToContent(msg *llmv1.LLMMessage) *genai.Content {
	c := &genai.Content{
		Role: RoleToGenai(msg.Role),
	}
	for _, b := range msg.Content {
		switch v := b.Block.(type) {
		case *threadv1.ContentBlock_Text:
			c.Parts = append(c.Parts, &genai.Part{Text: v.Text.Text})
		case *threadv1.ContentBlock_Thinking:
			c.Parts = append(c.Parts, &genai.Part{Text: v.Thinking.Text, Thought: true})
		case *threadv1.ContentBlock_ToolCall:
			var args map[string]any
			if v.ToolCall.Arguments != "" {
				json.Unmarshal([]byte(v.ToolCall.Arguments), &args)
			}
			c.Parts = append(c.Parts, &genai.Part{
				FunctionCall: &genai.FunctionCall{
					ID:   v.ToolCall.Id,
					Name: v.ToolCall.Name,
					Args: args,
				},
			})
		case *threadv1.ContentBlock_ToolResult:
			// Re-hydrate the stored JSON back into a map so the model
			// sees the original tool schema (e.g. `{answers: {...}}`
			// for AskUserQuestion), not a synthetic `{result: ""}`.
			// Falls back to `{result: <raw>}` when the content isn't
			// valid JSON (tools that return a plain string).
			var resp map[string]any
			if v.ToolResult.Content != "" {
				if err := json.Unmarshal([]byte(v.ToolResult.Content), &resp); err != nil || resp == nil {
					resp = map[string]any{"result": v.ToolResult.Content}
				}
			} else {
				resp = map[string]any{"result": ""}
			}
			c.Parts = append(c.Parts, &genai.Part{
				FunctionResponse: &genai.FunctionResponse{
					ID:       v.ToolResult.ToolCallId,
					Response: resp,
				},
			})
		}
	}
	return c
}

// ExtractThinking returns the thinking blocks from a genai Content.
func ExtractThinking(c *genai.Content) []*threadv1.ThinkingContent {
	var thinking []*threadv1.ThinkingContent
	if c == nil {
		return thinking
	}
	for _, p := range c.Parts {
		if p.Thought && p.Text != "" {
			thinking = append(thinking, &threadv1.ThinkingContent{Text: p.Text})
		}
	}
	return thinking
}

// RoleToProto maps a genai role string to the proto Role enum.
func RoleToProto(role string) threadv1.Role {
	switch role {
	case "user":
		return threadv1.Role_ROLE_USER
	case "model":
		return threadv1.Role_ROLE_ASSISTANT
	case "system":
		return threadv1.Role_ROLE_SYSTEM
	default:
		return threadv1.Role_ROLE_USER
	}
}

// RoleToGenai maps the proto Role enum to a genai role string. genai has no
// "system" role — the system instruction travels via
// GenerateContentConfig.SystemInstruction, not a content role — so SYSTEM
// maps to "user" as a carrier.
func RoleToGenai(role threadv1.Role) string {
	switch role {
	case threadv1.Role_ROLE_USER:
		return "user"
	case threadv1.Role_ROLE_ASSISTANT:
		return "model"
	case threadv1.Role_ROLE_SYSTEM:
		return "user"
	default:
		return "user"
	}
}
