package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/emontenegr/spidey/core"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// Codec implements core.Codec for OpenAI Chat Completions wire
// format. Both directions are covered: inbound (proxy decode) and
// outbound (provider encode), so this is the single authority for
// what an OpenAI-shaped request/response looks like.
//
// Stateless. Registered globally as core.Codec under the name
// "openai" via init() in register.go.
type Codec struct{}

// Name reports the registry key.
func (Codec) Name() string { return "openai" }

// DecodeRequest parses an OpenAI Chat Completions request body
// into the canonical CompletionRequest. Tool declarations,
// tool_choice, and tool-result messages are supported as far as
// the adapter currently models them; future fields propagate as
// they're added on either side.
func (Codec) DecodeRequest(body []byte) (*pb.CompletionRequest, error) {
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("openai decode request: %w", err)
	}
	llmMsgs := make([]*pb.LLMMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		llmMsgs = append(llmMsgs, fromChatMessageToLLM(m))
	}
	out := &pb.CompletionRequest{
		Model:       req.Model,
		Messages:    llmMsgs,
		Stream:      req.Stream,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
	}
	return out, nil
}

// EncodeRequest serializes a CompletionRequest into the OpenAI
// Chat Completions wire format. Used by the provider when calling
// upstream and by any external consumer that wants to emit
// OpenAI-shaped requests directly.
func (Codec) EncodeRequest(req *pb.CompletionRequest) ([]byte, error) {
	cr := toChatRequest(req.Model, req, req.Stream)
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(cr); err != nil {
		return nil, fmt.Errorf("openai encode request: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// EncodeResponse serializes a CompletionResponse into the OpenAI
// Chat Completions response shape — id / model / choices /
// usage. finish_reason flows through when the response carries
// one.
func (Codec) EncodeResponse(resp *pb.CompletionResponse) ([]byte, error) {
	finishReason := resp.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	choice := map[string]any{
		"index":         0,
		"message":       chatMessageFromLLM(resp.Message),
		"finish_reason": finishReason,
	}
	out := map[string]any{
		"id":      resp.Id,
		"object":  "chat.completion",
		"model":   resp.Model,
		"choices": []map[string]any{choice},
	}
	if resp.Usage != nil {
		out["usage"] = map[string]any{
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"total_tokens":      resp.Usage.PromptTokens + resp.Usage.CompletionTokens,
		}
	}
	return json.Marshal(out)
}

// DecodeResponse parses an upstream OpenAI Chat Completions JSON
// response into the canonical CompletionResponse.
func (Codec) DecodeResponse(body []byte) (*pb.CompletionResponse, error) {
	var resp chatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("openai decode response: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("openai: response had no choices")
	}
	return &pb.CompletionResponse{
		Id:    resp.ID,
		Model: resp.Model,
		Message: &pb.LLMMessage{
			Role:    pb.Role_ROLE_ASSISTANT,
			Content: fromChatMessage(resp.Choices[0].Message),
		},
		Usage: &pb.Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
		},
	}, nil
}

// EncodeChunk serializes one StreamChunk into the OpenAI SSE shape:
// "data: {...}\n\n" for content chunks, "data: [DONE]\n\n" on the
// terminal chunk. Returns the full line(s) including trailing
// newlines so the caller writes the bytes verbatim.
func (Codec) EncodeChunk(chunk *pb.StreamChunk) ([]byte, error) {
	if chunk.Done {
		return []byte("data: [DONE]\n\n"), nil
	}
	delta := map[string]any{}
	if t := chunk.GetText(); t != nil {
		delta["content"] = t.Text
	}
	if tc := chunk.GetToolCall(); tc != nil {
		delta["tool_calls"] = []map[string]any{{
			"id":   tc.Id,
			"type": "function",
			"function": map[string]any{
				"name":      tc.Name,
				"arguments": tc.Arguments,
			},
		}}
	}
	choice := map[string]any{
		"index": chunk.ChoiceIndex,
		"delta": delta,
	}
	if chunk.FinishReason != "" {
		choice["finish_reason"] = chunk.FinishReason
	}
	payload := map[string]any{
		"object":  "chat.completion.chunk",
		"choices": []map[string]any{choice},
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

// DecodeChunk parses one server-sent line from an upstream OpenAI
// stream. Returns (nil, nil) for blank lines, comment lines, and
// keep-alives the caller should skip. The "data: [DONE]" sentinel
// produces a Done chunk.
func (Codec) DecodeChunk(line []byte) (*pb.StreamChunk, error) {
	s := string(bytes.TrimRight(line, "\r\n"))
	if s == "" || strings.HasPrefix(s, ":") {
		return nil, nil
	}
	if !strings.HasPrefix(s, "data: ") {
		return nil, nil
	}
	data := strings.TrimPrefix(s, "data: ")
	if data == "[DONE]" {
		return &pb.StreamChunk{Done: true}, nil
	}
	var chunk chatResponse
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return nil, fmt.Errorf("openai decode chunk: %w", err)
	}
	if len(chunk.Choices) == 0 {
		out := &pb.StreamChunk{}
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			out.Usage = &pb.Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
			}
		}
		return out, nil
	}
	choice := chunk.Choices[0]
	out := &pb.StreamChunk{
		ChoiceIndex: 0,
	}
	if s, ok := choice.Delta.Content.(string); ok && s != "" {
		out.Delta = &pb.StreamChunk_Text{Text: &pb.TextContent{Text: s}}
	}
	if len(choice.Delta.ToolCalls) > 0 {
		tc := choice.Delta.ToolCalls[0]
		out.Delta = &pb.StreamChunk_ToolCall{ToolCall: &pb.ToolCallContent{
			Id:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		}}
	}
	return out, nil
}

// fromChatMessageToLLM is the proxy-direction helper — given an
// inbound chatMessage from a client request, reconstruct the
// pb.LLMMessage. Mirrors fromChatMessage but produces an LLMMessage
// (role + content) rather than just a content slice.
func fromChatMessageToLLM(m chatMessage) *pb.LLMMessage {
	role := pb.Role_ROLE_USER
	switch strings.ToLower(m.Role) {
	case "assistant":
		role = pb.Role_ROLE_ASSISTANT
	case "system":
		role = pb.Role_ROLE_SYSTEM
	case "tool":
		role = pb.Role_ROLE_USER // OpenAI carries tool results as role=tool
	}
	out := &pb.LLMMessage{Role: role}
	if s, ok := m.Content.(string); ok && s != "" {
		out.Content = append(out.Content, &pb.ContentBlock{
			Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: s}},
		})
	}
	for _, tc := range m.ToolCalls {
		out.Content = append(out.Content, &pb.ContentBlock{
			Block: &pb.ContentBlock_ToolCall{ToolCall: &pb.ToolCallContent{
				Id:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			}},
		})
	}
	if m.ToolCallID != "" {
		// role=tool message — the content carries the tool result.
		var resultText string
		if s, ok := m.Content.(string); ok {
			resultText = s
		}
		out.Content = []*pb.ContentBlock{{
			Block: &pb.ContentBlock_ToolResult{ToolResult: &pb.ToolResultContent{
				ToolCallId: m.ToolCallID,
				Content:    resultText,
			}},
		}}
	}
	return out
}

// chatMessageFromLLM converts a canonical LLMMessage into the
// OpenAI response message shape. The receiver is always
// "assistant" in practice (the response message slot), so
// content/tool_calls are surfaced and tool_call_id is omitted.
func chatMessageFromLLM(m *pb.LLMMessage) map[string]any {
	if m == nil {
		return map[string]any{"role": "assistant", "content": ""}
	}
	out := map[string]any{"role": "assistant"}
	var sb strings.Builder
	var calls []map[string]any
	for _, b := range m.Content {
		switch v := b.Block.(type) {
		case *pb.ContentBlock_Text:
			sb.WriteString(v.Text.Text)
		case *pb.ContentBlock_ToolCall:
			calls = append(calls, map[string]any{
				"id":   v.ToolCall.Id,
				"type": "function",
				"function": map[string]any{
					"name":      v.ToolCall.Name,
					"arguments": v.ToolCall.Arguments,
				},
			})
		}
	}
	if sb.Len() > 0 {
		out["content"] = sb.String()
	} else {
		out["content"] = ""
	}
	if len(calls) > 0 {
		out["tool_calls"] = calls
	}
	return out
}

var _ core.Codec = Codec{}
