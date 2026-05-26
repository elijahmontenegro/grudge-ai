package adk

import (
	"encoding/json"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"google.golang.org/genai"
)

// --- genai <-> proto conversions ---

// GenaiContentToProto converts a single genai.Content to proto LLMMessage.
func GenaiContentToProto(c *genai.Content) *pb.LLMMessage {
	msg := &pb.LLMMessage{
		Role: genaiRoleToProto(c.Role),
	}
	for _, p := range c.Parts {
		if p.Text != "" {
			if p.Thought {
				msg.Content = append(msg.Content, &pb.ContentBlock{
					Block: &pb.ContentBlock_Thinking{Thinking: &pb.ThinkingContent{Text: p.Text}},
				})
			} else {
				msg.Content = append(msg.Content, &pb.ContentBlock{
					Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: p.Text}},
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
			msg.Content = append(msg.Content, &pb.ContentBlock{
				Block: &pb.ContentBlock_ToolCall{ToolCall: &pb.ToolCallContent{
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
			msg.Content = append(msg.Content, &pb.ContentBlock{
				Block: &pb.ContentBlock_ToolResult{ToolResult: &pb.ToolResultContent{
					ToolCallId: p.FunctionResponse.ID,
					Content:    respText,
				}},
			})
		}
	}
	return msg
}

// ProtoToGenaiContent converts a proto LLMMessage to genai.Content.
func ProtoToGenaiContent(msg *pb.LLMMessage) *genai.Content {
	c := &genai.Content{
		Role: protoRoleToGenai(msg.Role),
	}
	for _, b := range msg.Content {
		switch v := b.Block.(type) {
		case *pb.ContentBlock_Text:
			c.Parts = append(c.Parts, &genai.Part{Text: v.Text.Text})
		case *pb.ContentBlock_Thinking:
			c.Parts = append(c.Parts, &genai.Part{Text: v.Thinking.Text, Thought: true})
		case *pb.ContentBlock_ToolCall:
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
		case *pb.ContentBlock_ToolResult:
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

// ProtoResponseToGenai converts a proto CompletionResponse to genai LLMResponse.
func ProtoResponseToGenai(resp *pb.CompletionResponse) *genai.Content {
	return ProtoToGenaiContent(resp.Message)
}

// ExtractThinkingFromGenai extracts thinking blocks from genai Content.
func ExtractThinkingFromGenai(c *genai.Content) []*pb.ThinkingContent {
	var thinking []*pb.ThinkingContent
	if c == nil {
		return thinking
	}
	for _, p := range c.Parts {
		if p.Thought && p.Text != "" {
			thinking = append(thinking, &pb.ThinkingContent{Text: p.Text})
		}
	}
	return thinking
}

func genaiRoleToProto(role string) pb.Role {
	switch role {
	case "user":
		return pb.Role_ROLE_USER
	case "model":
		return pb.Role_ROLE_ASSISTANT
	case "system":
		return pb.Role_ROLE_SYSTEM
	default:
		return pb.Role_ROLE_USER
	}
}

// MarshalFunctionArgs serializes function call args to JSON.
func MarshalFunctionArgs(args map[string]any) (string, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return "{}", err
	}
	return string(b), nil
}

func protoRoleToGenai(role pb.Role) string {
	switch role {
	case pb.Role_ROLE_USER:
		return "user"
	case pb.Role_ROLE_ASSISTANT:
		return "model"
	case pb.Role_ROLE_SYSTEM:
		// genai has no "system" role. Map to "user" as a carrier —
		// the actual system instruction is handled separately via
		// req.Config.SystemInstruction, not through content roles.
		return "user"
	default:
		return "user"
	}
}
