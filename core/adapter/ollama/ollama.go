package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/internal/httpc"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// Config for the Ollama provider. No auth — Ollama runs locally.
type Config struct {
	BaseURL string // e.g., "http://localhost:11434"
}

type provider struct {
	cfg    Config
	client *httpc.Client
}

// New creates an Ollama provider.
func New(cfg Config) core.Provider {
	return &provider{
		cfg:    cfg,
		client: httpc.New(httpc.TimeoutDefault, nil),
	}
}

func (p *provider) ID() string { return "ollama" }

func (p *provider) Completer(model string) (core.Completer, error) {
	return &completer{
		model:         model,
		baseURL:       p.cfg.BaseURL,
		client:        p.client,
		streamClient:  httpc.New(httpc.TimeoutStreaming, nil),
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

func (p *provider) Close() error { return nil }

// --- Completer ---

type completer struct {
	model        string
	baseURL      string
	client       *httpc.Client
	streamClient *httpc.Client
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Options  any           `json:"options,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Message      chatMessage `json:"message"`
	Model        string      `json:"model"`
	Done         bool        `json:"done"`
	PromptEval   int         `json:"prompt_eval_count"`
	EvalCount    int         `json:"eval_count"`
}

func (c *completer) Complete(ctx context.Context, req *pb.CompletionRequest) (*pb.CompletionResponse, error) {
	body, err := json.Marshal(chatRequest{
		Model:    c.model,
		Messages: toLlamaMsgs(req.Messages),
		Stream:   false,
		Options:  providerOpts(req),
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := c.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: ollama returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp chatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}

	return &pb.CompletionResponse{
		Message: &pb.LLMMessage{
			Role:    pb.Role_ROLE_ASSISTANT,
			Content: []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: resp.Message.Content}}}},
		},
		Usage: &pb.Usage{
			PromptTokens:     int32(resp.PromptEval),
			CompletionTokens: int32(resp.EvalCount),
		},
		Model: resp.Model,
	}, nil
}

func (c *completer) Stream(ctx context.Context, req *pb.CompletionRequest) (<-chan *pb.StreamChunk, error) {
	body, err := json.Marshal(chatRequest{
		Model:    c.model,
		Messages: toLlamaMsgs(req.Messages),
		Stream:   true,
		Options:  providerOpts(req),
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/chat", bytes.NewReader(body))
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
		return nil, fmt.Errorf("%w: ollama returned %d", core.ErrProviderUnavailable, resp.StatusCode)
	}

	ch := make(chan *pb.StreamChunk)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		dec := json.NewDecoder(resp.Body)
		for {
			var chunk chatResponse
			if err := dec.Decode(&chunk); err != nil {
				if err != io.EOF {
					ch <- &pb.StreamChunk{Done: true, Error: ptr(err.Error())}
				}
				return
			}
			if chunk.Done {
				ch <- &pb.StreamChunk{
					Done: true,
					Usage: &pb.Usage{
						PromptTokens:     int32(chunk.PromptEval),
						CompletionTokens: int32(chunk.EvalCount),
					},
				}
				return
			}
			ch <- &pb.StreamChunk{
				Delta: &pb.StreamChunk_Text{Text: &pb.TextContent{Text: chunk.Message.Content}},
			}
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
	Input string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func (e *embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: e.model, Input: text})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", e.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := e.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: ollama returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp embedResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	if len(resp.Embeddings) == 0 {
		return nil, fmt.Errorf("%w: ollama returned empty embeddings", core.ErrProviderUnavailable)
	}
	return resp.Embeddings[0], nil
}

func (e *embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := e.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		results[i] = vec
	}
	return results, nil
}

// --- helpers ---

func toLlamaMsgs(msgs []*pb.LLMMessage) []chatMessage {
	out := make([]chatMessage, len(msgs))
	for i, m := range msgs {
		out[i] = chatMessage{
			Role:    roleStr(m.Role),
			Content: textFromBlocks(m.Content),
		}
	}
	return out
}

func roleStr(r pb.Role) string {
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
	var s string
	for _, b := range blocks {
		if t := b.GetText(); t != nil {
			s += t.Text
		}
	}
	return s
}

func providerOpts(req *pb.CompletionRequest) map[string]any {
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
	if len(opts) == 0 {
		return nil
	}
	return opts
}

func ptr(s string) *string { return &s }
