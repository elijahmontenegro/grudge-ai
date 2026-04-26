package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/internal/httpc"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// Config for the OpenAI provider. Configurable BaseURL supports any
// OpenAI-compatible endpoint.
type Config struct {
	APIKey  string
	BaseURL string // default: "https://api.openai.com"
}

type provider struct {
	cfg    Config
	client *httpc.Client
}

// New creates an OpenAI provider (or any OpenAI-compatible endpoint).
func New(cfg Config) core.Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com"
	}
	authFn := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	return &provider{
		cfg:    cfg,
		client: httpc.New(httpc.TimeoutDefault, authFn),
	}
}


func (p *provider) Completer(model string) (core.Completer, error) {
	authFn := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	return &completer{
		model:        model,
		baseURL:      p.cfg.BaseURL,
		client:       p.client,
		streamClient: httpc.NewStreaming(authFn),
	}, nil
}

func (p *provider) Embedder(model string) (core.Embedder, error) {
	return &embedder{
		model:   model,
		baseURL: p.cfg.BaseURL,
		client:  p.client,
	}, nil
}

func (p *provider) Classifier(_ string) (core.Classifier, error) {
	return nil, core.ErrUnsupported
}


// --- Completer ---

type completer struct {
	model        string
	baseURL      string
	client       *httpc.Client
	streamClient *httpc.Client
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   *int32        `json:"max_tokens,omitempty"`
	Temperature *float32      `json:"temperature,omitempty"`
	TopP        *float32      `json:"top_p,omitempty"`
	Stop        []string      `json:"stop,omitempty"`
	Stream      bool          `json:"stream"`
}

type chatMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content"`
	ToolCalls  []toolCall    `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatResponse struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   apiUsage     `json:"usage"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
	Delta   chatMessage `json:"delta"`
}

type apiUsage struct {
	PromptTokens     int32 `json:"prompt_tokens"`
	CompletionTokens int32 `json:"completion_tokens"`
}

func (c *completer) Complete(ctx context.Context, req *pb.CompletionRequest) (*pb.CompletionResponse, error) {
	body, err := json.Marshal(toChatRequest(c.model, req, false))
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := c.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: openai returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp chatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("%w: openai returned no choices", core.ErrProviderUnavailable)
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

func (c *completer) Stream(ctx context.Context, req *pb.CompletionRequest) (<-chan *pb.StreamChunk, error) {
	body, err := json.Marshal(toChatRequest(c.model, req, true))
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.streamClient.Do(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &httpc.StatusError{Provider: "openai", StatusCode: resp.StatusCode}
	}

	ch := make(chan *pb.StreamChunk)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		var totalUsage *pb.Usage
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := line[6:]
			if data == "[DONE]" {
				ch <- &pb.StreamChunk{Done: true, Usage: totalUsage}
				return
			}

			var chunk chatResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				ch <- &pb.StreamChunk{Done: true, Error: ptr(err.Error())}
				return
			}

			if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
				totalUsage = &pb.Usage{
					PromptTokens:     chunk.Usage.PromptTokens,
					CompletionTokens: chunk.Usage.CompletionTokens,
				}
			}

			if len(chunk.Choices) == 0 {
				continue
			}

			delta := chunk.Choices[0].Delta
			content, _ := delta.Content.(string)
			if content != "" {
				ch <- &pb.StreamChunk{
					Delta: &pb.StreamChunk_Text{Text: &pb.TextContent{Text: content}},
				}
			}
			for _, tc := range delta.ToolCalls {
				ch <- &pb.StreamChunk{
					Delta: &pb.StreamChunk_ToolCall{ToolCall: &pb.ToolCallContent{
						Id:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					}},
				}
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- &pb.StreamChunk{Done: true, Error: ptr(err.Error())}
		}
	}()
	return ch, nil
}

// --- Embedder ---

type embedder struct {
	model   string
	baseURL string
	client  *httpc.Client
}

type embedRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"`
}

type embedResponse struct {
	Data []embedData `json:"data"`
}

type embedData struct {
	Embedding []float32 `json:"embedding"`
}

func (e *embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: e.model, Input: text})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", e.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := e.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: openai returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp embedResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("%w: openai returned empty embedding", core.ErrProviderUnavailable)
	}
	return resp.Data[0].Embedding, nil
}

func (e *embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", e.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := e.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: openai returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp embedResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("%w: openai returned %d embeddings for %d inputs", core.ErrProviderUnavailable, len(resp.Data), len(texts))
	}

	results := make([][]float32, len(resp.Data))
	for i, d := range resp.Data {
		results[i] = d.Embedding
	}
	return results, nil
}

// --- helpers ---

func toChatRequest(model string, req *pb.CompletionRequest, stream bool) chatRequest {
	msgs := make([]chatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, toChatMessage(m))
	}
	return chatRequest{
		Model:       model,
		Messages:    msgs,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Stream:      stream,
	}
}

func toChatMessage(m *pb.LLMMessage) chatMessage {
	role := "user"
	switch m.Role {
	case pb.Role_ROLE_ASSISTANT:
		role = "assistant"
	case pb.Role_ROLE_SYSTEM:
		role = "system"
	}

	// If only text blocks, use simple string content.
	allText := true
	for _, b := range m.Content {
		if _, ok := b.Block.(*pb.ContentBlock_Text); !ok {
			allText = false
			break
		}
	}

	msg := chatMessage{Role: role}
	if allText {
		var sb strings.Builder
		for _, b := range m.Content {
			sb.WriteString(b.GetText().Text)
		}
		msg.Content = sb.String()
	} else {
		msg.Content = toMultipart(m.Content)
	}
	return msg
}

func toMultipart(blocks []*pb.ContentBlock) []map[string]any {
	parts := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		switch v := b.Block.(type) {
		case *pb.ContentBlock_Text:
			parts = append(parts, map[string]any{"type": "text", "text": v.Text.Text})
		}
	}
	return parts
}

func fromChatMessage(m chatMessage) []*pb.ContentBlock {
	var blocks []*pb.ContentBlock
	if s, ok := m.Content.(string); ok && s != "" {
		blocks = append(blocks, &pb.ContentBlock{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: s}}})
	}
	for _, tc := range m.ToolCalls {
		blocks = append(blocks, &pb.ContentBlock{Block: &pb.ContentBlock_ToolCall{ToolCall: &pb.ToolCallContent{
			Id:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		}}})
	}
	return blocks
}

func ptr(s string) *string { return &s }
