package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/core/adapter/internal/util"
	"github.com/elijahmontenegro/grudge/core/httpc"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// Config for the OpenAI provider. Configurable BaseURL supports any
// OpenAI-compatible endpoint.
type Config struct {
	APIKey  string
	BaseURL string // default: "https://api.openai.com"
}

type provider struct {
	cfg    Config
	authFn func(*http.Request)
	client *httpc.Client
}

// New creates an OpenAI provider (or any OpenAI-compatible endpoint).
func New(cfg Config) any {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com"
	}
	p := &provider{cfg: cfg}
	// Skip the Authorization header entirely when no key is set. An empty
	// key means "no auth" (local vLLM / Ollama-compatible servers, which
	// reject a literal `Bearer ` with an empty token) — not `Bearer `.
	p.authFn = func(req *http.Request) {
		if cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		}
	}
	p.client = httpc.New(httpc.TimeoutDefault, p.authFn)
	return p
}

func (p *provider) Completer(model string) (core.Completer, error) {
	return &completer{
		model:        model,
		baseURL:      p.cfg.BaseURL,
		client:       p.client,
		streamClient: httpc.NewStreaming(p.authFn),
	}, nil
}

func (p *provider) Embedder(model string) (core.Embedder, error) {
	return &embedder{
		model:   model,
		baseURL: p.cfg.BaseURL,
		client:  p.client,
	}, nil
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
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
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

func (c *completer) Complete(ctx context.Context, req *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error) {
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

	return &llmv1.CompletionResponse{
		Id:    resp.ID,
		Model: resp.Model,
		Message: &llmv1.LLMMessage{
			Role:    threadv1.Role_ROLE_ASSISTANT,
			Content: fromChatMessage(resp.Choices[0].Message),
		},
		Usage: &llmv1.Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
		},
	}, nil
}

func (c *completer) Stream(ctx context.Context, req *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error] {
	return func(yield func(*llmv1.StreamChunk, error) bool) {
		body, err := json.Marshal(toChatRequest(c.model, req, true))
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq, err := http.NewRequest("POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := c.streamClient.Do(ctx, httpReq)
		if err != nil {
			yield(nil, err)
			return
		}
		if resp.StatusCode != http.StatusOK {
			err := httpc.NewStatusError("openai", resp)
			resp.Body.Close()
			yield(nil, err)
			return
		}
		defer resp.Body.Close()

		var totalUsage *llmv1.Usage
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := line[6:]
			if data == "[DONE]" {
				yield(&llmv1.StreamChunk{Done: true, Usage: totalUsage}, nil)
				return
			}

			var chunk chatResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				yield(&llmv1.StreamChunk{Done: true, Error: util.Ptr(err.Error())}, nil)
				return
			}

			if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
				totalUsage = &llmv1.Usage{
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
				if !yield(&llmv1.StreamChunk{
					Delta: &llmv1.StreamChunk_Text{Text: &threadv1.TextContent{Text: content}},
				}, nil) {
					return
				}
			}
			for _, tc := range delta.ToolCalls {
				if !yield(&llmv1.StreamChunk{
					Delta: &llmv1.StreamChunk_ToolCall{ToolCall: &threadv1.ToolCallContent{
						Id:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					}},
				}, nil) {
					return
				}
			}
		}
		if err := scanner.Err(); err != nil {
			yield(&llmv1.StreamChunk{Done: true, Error: util.Ptr(err.Error())}, nil)
		}
	}
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

// Embed routes both roles to the same /v1/embeddings endpoint —
// OpenAI's embedding API is symmetric at the request level. The
// model itself may apply different prompts internally based on the
// model variant; nothing to encode at this adapter layer.
func (e *embedder) Embed(ctx context.Context, _ core.EmbedRole, texts []string) ([][]float32, error) {
	return e.embed(ctx, texts)
}

func (e *embedder) embed(ctx context.Context, texts []string) ([][]float32, error) {
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

func toChatRequest(model string, req *llmv1.CompletionRequest, stream bool) chatRequest {
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

func toChatMessage(m *llmv1.LLMMessage) chatMessage {
	role := "user"
	switch m.Role {
	case threadv1.Role_ROLE_ASSISTANT:
		role = "assistant"
	case threadv1.Role_ROLE_SYSTEM:
		role = "system"
	}

	// If only text blocks, use simple string content.
	allText := true
	for _, b := range m.Content {
		if _, ok := b.Block.(*threadv1.ContentBlock_Text); !ok {
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

func toMultipart(blocks []*threadv1.ContentBlock) []map[string]any {
	parts := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		switch v := b.Block.(type) {
		case *threadv1.ContentBlock_Text:
			parts = append(parts, map[string]any{"type": "text", "text": v.Text.Text})
		}
	}
	return parts
}

func fromChatMessage(m chatMessage) []*threadv1.ContentBlock {
	var blocks []*threadv1.ContentBlock
	if s, ok := m.Content.(string); ok && s != "" {
		blocks = append(blocks, &threadv1.ContentBlock{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: s}}})
	}
	for _, tc := range m.ToolCalls {
		blocks = append(blocks, &threadv1.ContentBlock{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{
			Id:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		}}})
	}
	return blocks
}
