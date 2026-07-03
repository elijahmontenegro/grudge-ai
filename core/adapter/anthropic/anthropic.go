package anthropic

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

const apiVersion = "2023-06-01"

// Config for the Anthropic provider.
type Config struct {
	APIKey  string
	BaseURL string // default: "https://api.anthropic.com"
}

type provider struct {
	cfg    Config
	client *httpc.Client
}

// New creates an Anthropic provider.
func New(cfg Config) any {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.anthropic.com"
	}
	authFn := func(req *http.Request) {
		req.Header.Set("x-api-key", cfg.APIKey)
		req.Header.Set("anthropic-version", apiVersion)
	}
	return &provider{
		cfg:    cfg,
		client: httpc.New(httpc.TimeoutDefault, authFn),
	}
}

func (p *provider) Completer(model string) (core.Completer, error) {
	authFn := func(req *http.Request) {
		req.Header.Set("x-api-key", p.cfg.APIKey)
		req.Header.Set("anthropic-version", apiVersion)
	}
	return &completer{
		model:        model,
		baseURL:      p.cfg.BaseURL,
		client:       p.client,
		streamClient: httpc.NewStreaming(authFn),
	}, nil
}

// --- Completer ---

type completer struct {
	model        string
	baseURL      string
	client       *httpc.Client
	streamClient *httpc.Client
}

type messagesRequest struct {
	Model     string       `json:"model"`
	Messages  []apiMessage `json:"messages"`
	MaxTokens int32        `json:"max_tokens"`
	System    string       `json:"system,omitempty"`
	Stream    bool         `json:"stream"`
}

type apiMessage struct {
	Role    string           `json:"role"`
	Content []apiContentPart `json:"content"`
}

type apiContentPart struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Input   string `json:"input,omitempty"`
	ToolID  string `json:"tool_use_id,omitempty"`
	Content string `json:"content,omitempty"`
}

type messagesResponse struct {
	ID      string           `json:"id"`
	Content []apiContentPart `json:"content"`
	Model   string           `json:"model"`
	Usage   apiUsage         `json:"usage"`
}

type apiUsage struct {
	InputTokens  int32 `json:"input_tokens"`
	OutputTokens int32 `json:"output_tokens"`
}

func (c *completer) Complete(ctx context.Context, req *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error) {
	apiReq := toAPIRequest(c.model, req, false)

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := c.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: anthropic returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp messagesResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}

	return &llmv1.CompletionResponse{
		Id:    resp.ID,
		Model: resp.Model,
		Message: &llmv1.LLMMessage{
			Role:    threadv1.Role_ROLE_ASSISTANT,
			Content: fromAPIContent(resp.Content),
		},
		Usage: &llmv1.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
		},
	}, nil
}

func (c *completer) Stream(ctx context.Context, req *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error] {
	return func(yield func(*llmv1.StreamChunk, error) bool) {
		apiReq := toAPIRequest(c.model, req, true)
		body, err := json.Marshal(apiReq)
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq, err := http.NewRequest("POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
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
			err := httpc.NewStatusError("anthropic", resp)
			resp.Body.Close()
			yield(nil, err)
			return
		}
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := line[6:]
			if data == "[DONE]" {
				return
			}

			var event sseEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				yield(&llmv1.StreamChunk{Done: true, Error: util.Ptr(err.Error())}, nil)
				return
			}

			switch event.Type {
			case "content_block_delta":
				if event.Delta.Type == "text_delta" {
					if !yield(&llmv1.StreamChunk{
						Delta: &llmv1.StreamChunk_Text{Text: &threadv1.TextContent{Text: event.Delta.Text}},
					}, nil) {
						return
					}
				} else if event.Delta.Type == "thinking_delta" {
					if !yield(&llmv1.StreamChunk{
						Delta: &llmv1.StreamChunk_Thinking{Thinking: &threadv1.ThinkingContent{Text: event.Delta.Thinking}},
					}, nil) {
						return
					}
				}
			case "message_delta":
				yield(&llmv1.StreamChunk{
					Done: true,
					Usage: &llmv1.Usage{
						PromptTokens:     event.Usage.InputTokens,
						CompletionTokens: event.Usage.OutputTokens,
					},
				}, nil)
				return
			case "error":
				yield(&llmv1.StreamChunk{Done: true, Error: util.Ptr(event.Error.Message)}, nil)
				return
			}
		}
		if err := scanner.Err(); err != nil {
			yield(&llmv1.StreamChunk{Done: true, Error: util.Ptr(err.Error())}, nil)
		}
	}
}

type sseEvent struct {
	Type  string   `json:"type"`
	Delta sseDelta `json:"delta,omitempty"`
	Usage apiUsage `json:"usage,omitempty"`
	Error sseError `json:"error,omitempty"`
}

type sseDelta struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

type sseError struct {
	Message string `json:"message"`
}

// --- helpers ---

func toAPIRequest(model string, req *llmv1.CompletionRequest, stream bool) messagesRequest {
	var system string
	var msgs []apiMessage
	for _, m := range req.Messages {
		if m.Role == threadv1.Role_ROLE_SYSTEM {
			system = textFromBlocks(m.Content)
			continue
		}
		msgs = append(msgs, apiMessage{
			Role:    roleStr(m.Role),
			Content: toAPIContent(m.Content),
		})
	}

	maxTokens := int32(4096)
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	return messagesRequest{
		Model:     model,
		Messages:  msgs,
		MaxTokens: maxTokens,
		System:    system,
		Stream:    stream,
	}
}

func toAPIContent(blocks []*threadv1.ContentBlock) []apiContentPart {
	parts := make([]apiContentPart, 0, len(blocks))
	for _, b := range blocks {
		switch v := b.Block.(type) {
		case *threadv1.ContentBlock_Text:
			parts = append(parts, apiContentPart{Type: "text", Text: v.Text.Text})
		case *threadv1.ContentBlock_ToolCall:
			parts = append(parts, apiContentPart{Type: "tool_use", ID: v.ToolCall.Id, Name: v.ToolCall.Name, Input: v.ToolCall.Arguments})
		case *threadv1.ContentBlock_ToolResult:
			parts = append(parts, apiContentPart{Type: "tool_result", ToolID: v.ToolResult.ToolCallId, Content: v.ToolResult.Content})
		}
	}
	return parts
}

func fromAPIContent(parts []apiContentPart) []*threadv1.ContentBlock {
	blocks := make([]*threadv1.ContentBlock, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text":
			blocks = append(blocks, &threadv1.ContentBlock{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: p.Text}}})
		case "thinking":
			blocks = append(blocks, &threadv1.ContentBlock{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: p.Text}}})
		}
	}
	return blocks
}

func roleStr(r threadv1.Role) string {
	switch r {
	case threadv1.Role_ROLE_USER:
		return "user"
	case threadv1.Role_ROLE_ASSISTANT:
		return "assistant"
	default:
		return "user"
	}
}

func textFromBlocks(blocks []*threadv1.ContentBlock) string {
	var s string
	for _, b := range blocks {
		if t := b.GetText(); t != nil {
			s += t.Text
		}
	}
	return s
}
