package adapter

import (
	"strings"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"google.golang.org/genai"
)

// ProtoToText extracts all text content from proto content blocks.
func ProtoToText(blocks []*pb.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if t := b.GetText(); t != nil {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}

// TextToProto wraps a text string as a proto content block.
func TextToProto(text string) []*pb.ContentBlock {
	return []*pb.ContentBlock{
		{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: text}}},
	}
}

// MessageToLLM converts a corpus Message to an LLMMessage (wire format).
func MessageToLLM(msg *pb.Message) *pb.LLMMessage {
	return &pb.LLMMessage{
		Role:    msg.Role,
		Content: msg.Content,
	}
}

// MessagesToLLM converts a slice of Messages to LLMMessages.
func MessagesToLLM(msgs []*pb.Message) []*pb.LLMMessage {
	result := make([]*pb.LLMMessage, len(msgs))
	for i, m := range msgs {
		result[i] = MessageToLLM(m)
	}
	return result
}

// --- genai <-> proto conversions ---

// GenaiToProtoMessages converts genai.Content slices to proto LLMMessages.
func GenaiToProtoMessages(contents []*genai.Content) []*pb.LLMMessage {
	var msgs []*pb.LLMMessage
	for _, c := range contents {
		msgs = append(msgs, GenaiContentToProto(c))
	}
	return msgs
}

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
			argsJSON := ""
			if p.FunctionCall.Args != nil {
				// Args is map[string]any — serialize to JSON string
				var sb strings.Builder
				sb.WriteString("{")
				first := true
				for k, v := range p.FunctionCall.Args {
					if !first {
						sb.WriteString(",")
					}
					sb.WriteString(`"` + k + `":"`)
					if s, ok := v.(string); ok {
						sb.WriteString(s)
					}
					sb.WriteString(`"`)
					first = false
				}
				sb.WriteString("}")
				argsJSON = sb.String()
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
			respText := ""
			if p.FunctionResponse.Response != nil {
				if s, ok := p.FunctionResponse.Response["result"].(string); ok {
					respText = s
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
			c.Parts = append(c.Parts, &genai.Part{
				FunctionCall: &genai.FunctionCall{
					ID:   v.ToolCall.Id,
					Name: v.ToolCall.Name,
				},
			})
		case *pb.ContentBlock_ToolResult:
			c.Parts = append(c.Parts, &genai.Part{
				FunctionResponse: &genai.FunctionResponse{
					ID: v.ToolResult.ToolCallId,
					Response: map[string]any{
						"result": v.ToolResult.Content,
					},
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

func protoRoleToGenai(role pb.Role) string {
	switch role {
	case pb.Role_ROLE_USER:
		return "user"
	case pb.Role_ROLE_ASSISTANT:
		return "model"
	case pb.Role_ROLE_SYSTEM:
		return "user" // genai doesn't have system role — prepend to first user message
	default:
		return "user"
	}
}
