package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/emontenegr/spidey/core"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
)

// Handler handles proxy path requests. Stateless — each request creates an
// ephemeral RRC engine, scores, selects, assembles, and forwards.
type Handler struct {
	classifier core.Classifier
	completer  core.Completer
	rrcCfg     rrc.EngineConfig
}

// NewHandler creates a proxy handler.
func NewHandler(classifier core.Classifier, completer core.Completer, cfg rrc.EngineConfig) *Handler {
	return &Handler{
		classifier: classifier,
		completer:  completer,
		rrcCfg:     cfg,
	}
}

// RegisterRoutes registers proxy endpoints on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", h.handleOpenAI)
	mux.HandleFunc("POST /v1/messages", h.handleAnthropic)
}

func (h *Handler) handleOpenAI(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var req openAIChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "parse body: "+err.Error(), http.StatusBadRequest)
		return
	}

	messages := openAIToProto(req.Messages)
	selected, err := h.runStatelessRRC(r.Context(), messages)
	if err != nil {
		http.Error(w, "rrc: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Replace messages with RRC selection
	req.Messages = protoToOpenAI(selected)

	// Forward to upstream (the completer handles this)
	assembled := &pb.CompletionRequest{
		Messages: protoToLLM(selected),
		Model:    req.Model,
		Stream:   req.Stream,
	}

	if req.Stream {
		h.streamOpenAI(r.Context(), w, assembled)
	} else {
		h.completeOpenAI(r.Context(), w, assembled)
	}
}

func (h *Handler) handleAnthropic(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "parse body: "+err.Error(), http.StatusBadRequest)
		return
	}

	messages := anthropicToProto(req.Messages)
	selected, err := h.runStatelessRRC(r.Context(), messages)
	if err != nil {
		http.Error(w, "rrc: "+err.Error(), http.StatusBadGateway)
		return
	}

	assembled := &pb.CompletionRequest{
		Messages: protoToLLM(selected),
		Model:    req.Model,
		Stream:   req.Stream,
	}

	if req.Stream {
		h.streamAnthropic(r.Context(), w, assembled)
	} else {
		h.completeAnthropic(r.Context(), w, assembled)
	}
}

// runStatelessRRC creates an ephemeral engine, scores all messages, selects for
// the last message (the prompt), and returns the selected messages.
func (h *Handler) runStatelessRRC(ctx context.Context, messages []*pb.Message) ([]*pb.Message, error) {
	engine := rrc.NewEngine(h.rrcCfg, h.classifier)

	// Score each message against its predecessors
	for i, msg := range messages {
		if i == 0 {
			continue
		}
		corpus := messages[:i]
		if _, err := engine.OnMessage(ctx, msg, corpus); err != nil {
			return nil, fmt.Errorf("scoring message %d: %w", i, err)
		}
	}

	// Select for the last message (the prompt)
	prompt := messages[len(messages)-1]
	result, err := engine.Select(prompt.Id, pb.SelectionScope_SELECTION_SCOPE_THREAD, "proxy")
	if err != nil {
		return nil, fmt.Errorf("selection: %w", err)
	}

	// Build selected message list + the prompt
	selectedIDs := make(map[string]bool)
	for _, s := range result.Selected {
		selectedIDs[s.MessageId] = true
	}

	var selected []*pb.Message
	for _, msg := range messages {
		if selectedIDs[msg.Id] || msg.Id == prompt.Id {
			selected = append(selected, msg)
		}
	}

	// Zero selection is valid — just send the prompt
	if len(selected) == 0 {
		selected = []*pb.Message{prompt}
	}

	return selected, nil
}

func (h *Handler) completeOpenAI(ctx context.Context, w http.ResponseWriter, req *pb.CompletionRequest) {
	resp, err := h.completeWithBackoff(ctx, req)
	if err != nil {
		writeProxyError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(protoToOpenAIResponse(resp))
}

func (h *Handler) streamOpenAI(ctx context.Context, w http.ResponseWriter, req *pb.CompletionRequest) {
	ch, err := h.completer.(core.Completer).Stream(ctx, req)
	if err != nil {
		writeProxyError(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	for chunk := range ch {
		data, _ := json.Marshal(protoToOpenAIChunk(chunk))
		fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
		if chunk.Done {
			fmt.Fprintf(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
	}
}

func (h *Handler) completeAnthropic(ctx context.Context, w http.ResponseWriter, req *pb.CompletionRequest) {
	resp, err := h.completeWithBackoff(ctx, req)
	if err != nil {
		writeProxyError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(protoToAnthropicResponse(resp))
}

func (h *Handler) streamAnthropic(ctx context.Context, w http.ResponseWriter, req *pb.CompletionRequest) {
	ch, err := h.completer.(core.Completer).Stream(ctx, req)
	if err != nil {
		writeProxyError(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	for chunk := range ch {
		data, _ := json.Marshal(protoToAnthropicEvent(chunk))
		fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
		if chunk.Done {
			return
		}
	}
}

// --- Wire format types ---

type openAIChatRequest struct {
	Model    string             `json:"model"`
	Messages []openAIChatMsg    `json:"messages"`
	Stream   bool               `json:"stream"`
}

type openAIChatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model    string            `json:"model"`
	Messages []anthropicMsg    `json:"messages"`
	Stream   bool              `json:"stream"`
}

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// --- Conversion helpers ---

func openAIToProto(msgs []openAIChatMsg) []*pb.Message {
	result := make([]*pb.Message, len(msgs))
	for i, m := range msgs {
		result[i] = &pb.Message{
			Id:       fmt.Sprintf("proxy-%d", i),
			Role:     parseRole(m.Role),
			Content:  []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: m.Content}}}},
			Position: int64(i),
			ThreadId: "proxy",
		}
	}
	return result
}

func anthropicToProto(msgs []anthropicMsg) []*pb.Message {
	result := make([]*pb.Message, len(msgs))
	for i, m := range msgs {
		result[i] = &pb.Message{
			Id:       fmt.Sprintf("proxy-%d", i),
			Role:     parseRole(m.Role),
			Content:  []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: m.Content}}}},
			Position: int64(i),
			ThreadId: "proxy",
		}
	}
	return result
}

func protoToOpenAI(msgs []*pb.Message) []openAIChatMsg {
	result := make([]openAIChatMsg, len(msgs))
	for i, m := range msgs {
		result[i] = openAIChatMsg{
			Role:    roleString(m.Role),
			Content: textFromBlocks(m.Content),
		}
	}
	return result
}

func protoToLLM(msgs []*pb.Message) []*pb.LLMMessage {
	result := make([]*pb.LLMMessage, len(msgs))
	for i, m := range msgs {
		result[i] = &pb.LLMMessage{Role: m.Role, Content: m.Content}
	}
	return result
}

func protoToOpenAIResponse(resp *pb.CompletionResponse) map[string]any {
	text := ""
	if resp.Message != nil {
		text = textFromLLMBlocks(resp.Message.Content)
	}
	return map[string]any{
		"id":    resp.Id,
		"model": resp.Model,
		"choices": []map[string]any{{
			"message": map[string]any{
				"role":    "assistant",
				"content": text,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     resp.Usage.GetPromptTokens(),
			"completion_tokens": resp.Usage.GetCompletionTokens(),
		},
	}
}

func protoToOpenAIChunk(chunk *pb.StreamChunk) map[string]any {
	delta := map[string]any{}
	if t := chunk.GetText(); t != nil {
		delta["content"] = t.Text
	}
	return map[string]any{
		"choices": []map[string]any{{
			"delta": delta,
		}},
	}
}

func protoToAnthropicResponse(resp *pb.CompletionResponse) map[string]any {
	var content []map[string]any
	if resp.Message != nil {
		for _, b := range resp.Message.Content {
			if t := b.GetText(); t != nil {
				content = append(content, map[string]any{"type": "text", "text": t.Text})
			}
		}
	}
	return map[string]any{
		"id":      resp.Id,
		"model":   resp.Model,
		"content": content,
		"usage": map[string]any{
			"input_tokens":  resp.Usage.GetPromptTokens(),
			"output_tokens": resp.Usage.GetCompletionTokens(),
		},
	}
}

func protoToAnthropicEvent(chunk *pb.StreamChunk) map[string]any {
	if chunk.Done {
		return map[string]any{
			"type": "message_delta",
			"usage": map[string]any{
				"output_tokens": chunk.Usage.GetCompletionTokens(),
			},
		}
	}
	if t := chunk.GetText(); t != nil {
		return map[string]any{
			"type":  "content_block_delta",
			"delta": map[string]any{"type": "text_delta", "text": t.Text},
		}
	}
	return map[string]any{"type": "ping"}
}

// completeWithBackoff retries completion with fewer messages on context-length errors.
func (h *Handler) completeWithBackoff(ctx context.Context, req *pb.CompletionRequest) (*pb.CompletionResponse, error) {
	for {
		resp, err := h.completer.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		if !isContextLengthErr(err) || len(req.Messages) <= 1 {
			return nil, err
		}
		// Drop the first message (lowest priority — prompt is last)
		req.Messages = req.Messages[1:]
	}
}

// writeProxyError maps provider errors to appropriate HTTP status codes.
// 502: provider unreachable. 504: timeout. 422: context-length after backoff.
func writeProxyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrProviderUnavailable):
		http.Error(w, err.Error(), http.StatusBadGateway) // 502
	case errors.Is(err, core.ErrAuth):
		http.Error(w, err.Error(), http.StatusBadGateway) // 502
	case errors.Is(err, core.ErrRateLimited):
		http.Error(w, err.Error(), http.StatusTooManyRequests) // 429
	case isContextLengthErr(err):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity) // 422
	case errors.Is(err, context.DeadlineExceeded):
		http.Error(w, err.Error(), http.StatusGatewayTimeout) // 504
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

func isContextLengthErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "context_length") ||
		strings.Contains(msg, "maximum context") ||
		strings.Contains(msg, "too many tokens")
}

func parseRole(s string) pb.Role {
	switch strings.ToLower(s) {
	case "user":
		return pb.Role_ROLE_USER
	case "assistant":
		return pb.Role_ROLE_ASSISTANT
	case "system":
		return pb.Role_ROLE_SYSTEM
	default:
		return pb.Role_ROLE_USER
	}
}

func roleString(r pb.Role) string {
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

func textFromBlocks(blocks []*pb.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if t := b.GetText(); t != nil {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}

func textFromLLMBlocks(blocks []*pb.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if t := b.GetText(); t != nil {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}
