package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/elijahmontenegro/grudge/core"
	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
)

// Codec implements core.Codec for the Anthropic Messages wire
// format. Both directions: inbound (proxy decode) and outbound
// (provider encode). Stateless. Registered globally as core.Codec
// under "anthropic" via init() in register.go.
type Codec struct{}

// Name reports the registry key.
func (Codec) Name() string { return "anthropic" }

// DecodeRequest parses an Anthropic Messages request body into a
// canonical CompletionRequest. The system prompt is hoisted into a
// ROLE_SYSTEM LLMMessage at position 0 (Anthropic separates
// system; canonical proto uses a system role inline).
func (Codec) DecodeRequest(body []byte) (*pb.CompletionRequest, error) {
	var req messagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("anthropic decode request: %w", err)
	}
	out := &pb.CompletionRequest{
		Model:  req.Model,
		Stream: req.Stream,
	}
	if req.MaxTokens > 0 {
		mt := req.MaxTokens
		out.MaxTokens = &mt
	}
	if req.System != "" {
		out.Messages = append(out.Messages, &pb.LLMMessage{
			Role: pb.Role_ROLE_SYSTEM,
			Content: []*pb.ContentBlock{{
				Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: req.System}},
			}},
		})
	}
	for _, m := range req.Messages {
		llm := &pb.LLMMessage{
			Role:    roleFromAnthropic(m.Role),
			Content: fromAPIContent(m.Content),
		}
		out.Messages = append(out.Messages, llm)
	}
	return out, nil
}

// EncodeRequest serializes a CompletionRequest into the Anthropic
// Messages wire format.
func (Codec) EncodeRequest(req *pb.CompletionRequest) ([]byte, error) {
	apiReq := toAPIRequest(req.Model, req, req.Stream)
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(apiReq); err != nil {
		return nil, fmt.Errorf("anthropic encode request: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// EncodeResponse serializes a CompletionResponse into Anthropic's
// Messages response shape.
func (Codec) EncodeResponse(resp *pb.CompletionResponse) ([]byte, error) {
	out := map[string]any{
		"id":    resp.Id,
		"type":  "message",
		"role":  "assistant",
		"model": resp.Model,
	}
	if resp.Message != nil {
		out["content"] = toAPIContent(resp.Message.Content)
	}
	if resp.Usage != nil {
		out["usage"] = map[string]any{
			"input_tokens":  resp.Usage.PromptTokens,
			"output_tokens": resp.Usage.CompletionTokens,
		}
	}
	if resp.FinishReason != "" {
		out["stop_reason"] = resp.FinishReason
	}
	return json.Marshal(out)
}

// DecodeResponse parses an upstream Anthropic Messages JSON
// response into a canonical CompletionResponse.
func (Codec) DecodeResponse(body []byte) (*pb.CompletionResponse, error) {
	var resp messagesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("anthropic decode response: %w", err)
	}
	if resp.ID == "" && len(resp.Content) == 0 {
		return nil, errors.New("anthropic: empty response")
	}
	return &pb.CompletionResponse{
		Id:    resp.ID,
		Model: resp.Model,
		Message: &pb.LLMMessage{
			Role:    pb.Role_ROLE_ASSISTANT,
			Content: fromAPIContent(resp.Content),
		},
		Usage: &pb.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
		},
	}, nil
}

// EncodeChunk serializes one StreamChunk as an Anthropic SSE event.
// Anthropic uses different event types for delta vs. message_stop;
// the codec emits a content_block_delta for text content and a
// message_delta with stop_reason on done.
func (Codec) EncodeChunk(chunk *pb.StreamChunk) ([]byte, error) {
	var payload map[string]any
	switch {
	case chunk.Done:
		payload = map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason": chunk.FinishReason,
			},
		}
		if chunk.Usage != nil {
			payload["usage"] = map[string]any{
				"output_tokens": chunk.Usage.CompletionTokens,
			}
		}
	default:
		if t := chunk.GetText(); t != nil {
			payload = map[string]any{
				"type":  "content_block_delta",
				"index": chunk.ChoiceIndex,
				"delta": map[string]any{
					"type": "text_delta",
					"text": t.Text,
				},
			}
		} else if t := chunk.GetThinking(); t != nil {
			payload = map[string]any{
				"type":  "content_block_delta",
				"index": chunk.ChoiceIndex,
				"delta": map[string]any{
					"type":     "thinking_delta",
					"thinking": t.Text,
				},
			}
		} else {
			payload = map[string]any{"type": "ping"}
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(body)+8)
	out = append(out, []byte("data: ")...)
	out = append(out, body...)
	out = append(out, []byte("\n\n")...)
	return out, nil
}

// DecodeChunk parses one server-sent line from an upstream
// Anthropic stream. Returns (nil, nil) for blank lines, comments,
// and unrecognized event types the caller should skip.
func (Codec) DecodeChunk(line []byte) (*pb.StreamChunk, error) {
	s := string(bytes.TrimRight(line, "\r\n"))
	if s == "" || strings.HasPrefix(s, ":") {
		return nil, nil
	}
	if strings.HasPrefix(s, "event: ") {
		return nil, nil
	}
	if !strings.HasPrefix(s, "data: ") {
		return nil, nil
	}
	data := strings.TrimPrefix(s, "data: ")
	var ev sseEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return nil, fmt.Errorf("anthropic decode chunk: %w", err)
	}
	switch ev.Type {
	case "content_block_delta":
		if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
			return &pb.StreamChunk{
				Delta: &pb.StreamChunk_Text{Text: &pb.TextContent{Text: ev.Delta.Text}},
			}, nil
		}
		if ev.Delta.Type == "thinking_delta" && ev.Delta.Thinking != "" {
			return &pb.StreamChunk{
				Delta: &pb.StreamChunk_Thinking{Thinking: &pb.ThinkingContent{Text: ev.Delta.Thinking}},
			}, nil
		}
		return nil, nil
	case "message_delta":
		out := &pb.StreamChunk{}
		if ev.Usage.OutputTokens > 0 {
			out.Usage = &pb.Usage{
				PromptTokens:     ev.Usage.InputTokens,
				CompletionTokens: ev.Usage.OutputTokens,
			}
		}
		return out, nil
	case "message_stop":
		return &pb.StreamChunk{Done: true}, nil
	case "error":
		errMsg := ev.Error.Message
		return &pb.StreamChunk{Done: true, Error: &errMsg}, nil
	default:
		return nil, nil
	}
}

func roleFromAnthropic(r string) pb.Role {
	switch strings.ToLower(r) {
	case "assistant":
		return pb.Role_ROLE_ASSISTANT
	case "system":
		return pb.Role_ROLE_SYSTEM
	default:
		return pb.Role_ROLE_USER
	}
}

var _ core.Codec = Codec{}
