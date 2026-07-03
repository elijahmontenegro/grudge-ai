package ollama

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/core/adapter/internal/util"
	"github.com/elijahmontenegro/grudge/core/httpc"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// dumpBudget caps how many distinct 5xx request bodies we dump per
// process, to keep disk use bounded while still capturing a handful
// of cases for investigation. Same payload retried by the resilience
// loop only counts once (deduped on the first 32 bytes, which is
// enough to separate structurally-different requests).
const dumpBudgetMax = 10

var (
	dumpMu       sync.Mutex
	dumpCount    int
	dumpedHashes = make(map[string]bool)
)

// dumpFailingRequestOnce writes the given request body to a
// timestamped file. Dedupes on a prefix of the body so the same
// payload retried 10 times via exponential backoff produces one
// file; structurally-different failures each get their own.
func dumpFailingRequestOnce(body []byte) {
	dumpMu.Lock()
	defer dumpMu.Unlock()
	if dumpCount >= dumpBudgetMax {
		return
	}
	// Dedup on full-content hash. The system-prompt prefix is
	// constant across requests, so prefix-keying collapsed
	// structurally-different failures to one entry.
	h := sha1.Sum(body)
	key := hex.EncodeToString(h[:])
	if dumpedHashes[key] {
		return
	}
	dumpedHashes[key] = true
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("[Ollama] dump: UserHomeDir: %v", err)
		return
	}
	dir := filepath.Join(home, ".local", "share", "grudge", "failing-requests")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[Ollama] dump: MkdirAll: %v", err)
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("ollama-5xx-%s-%d.json", time.Now().Format("20060102-150405"), dumpCount))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		log.Printf("[Ollama] dump: WriteFile: %v", err)
		return
	}
	dumpCount++
	log.Printf("[Ollama] dump: failing request body written to %s (%d bytes, %d/%d budget)", path, len(body), dumpCount, dumpBudgetMax)
}

// Config for the Ollama provider. No auth — Ollama runs locally.
type Config struct {
	BaseURL string // e.g., "http://localhost:11434"
}

type provider struct {
	cfg    Config
	client *httpc.Client
}

// New creates an Ollama provider.
func New(cfg Config) any {
	return &provider{
		cfg:    cfg,
		client: httpc.New(httpc.TimeoutDefault, nil),
	}
}

func (p *provider) Completer(model string) (core.Completer, error) {
	return &completer{
		model:        model,
		baseURL:      p.cfg.BaseURL,
		client:       p.client,
		streamClient: httpc.NewStreaming(nil),
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

func (c *completer) Complete(ctx context.Context, req *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error) {
	cr := chatRequest{
		Model:    c.model,
		Messages: toLlamaMsgs(req.Messages),
		Stream:   false,
		Options:  providerOpts(req),
		Tools:    toOllamaTools(req.Tools),
	}
	body, err := json.Marshal(cr)
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
		log.Printf("[Ollama] Complete error %d: %d msgs, %d tools, body %d bytes, response: %s", status, len(cr.Messages), len(cr.Tools), len(body), string(respBody))
		return nil, fmt.Errorf("%w: ollama returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp chatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}

	msg := &llmv1.LLMMessage{Role: threadv1.Role_ROLE_ASSISTANT}
	if resp.Message.Thinking != "" {
		msg.Content = append(msg.Content, &threadv1.ContentBlock{
			Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: resp.Message.Thinking}},
		})
	}
	if resp.Message.Content != "" {
		msg.Content = append(msg.Content, &threadv1.ContentBlock{
			Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: resp.Message.Content}},
		})
	}
	for _, tc := range resp.Message.ToolCalls {
		argsStr := "{}"
		if len(tc.Function.Arguments) > 0 {
			argsStr = string(tc.Function.Arguments)
		}
		msg.Content = append(msg.Content, &threadv1.ContentBlock{
			Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{
				Id:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: argsStr,
			}},
		})
	}
	return &llmv1.CompletionResponse{
		Message: msg,
		Usage: &llmv1.Usage{
			PromptTokens:     int32(resp.PromptEval),
			CompletionTokens: int32(resp.EvalCount),
		},
		Model: resp.Model,
	}, nil
}

func (c *completer) Stream(ctx context.Context, req *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error] {
	return func(yield func(*llmv1.StreamChunk, error) bool) {
		cr := chatRequest{
			Model:    c.model,
			Messages: toLlamaMsgs(req.Messages),
			Stream:   true,
			Options:  providerOpts(req),
			Tools:    toOllamaTools(req.Tools),
		}
		body, err := json.Marshal(cr)
		if err != nil {
			yield(nil, err)
			return
		}

		log.Printf("[Ollama] Stream request: %d msgs, %d tools, %d bytes", len(cr.Messages), len(cr.Tools), len(body))

		httpReq, err := http.NewRequest("POST", c.baseURL+"/api/chat", bytes.NewReader(body))
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
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
			resp.Body.Close()
			log.Printf("[Ollama] Stream error %d: %d msgs, %d tools, body %d bytes, response: %s",
				resp.StatusCode, len(cr.Messages), len(cr.Tools), len(body), strings.TrimSpace(string(b)))
			if resp.StatusCode >= 500 {
				dumpFailingRequestOnce(body)
			}
			yield(nil, &httpc.StatusError{Provider: "ollama", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(b))})
			return
		}
		defer resp.Body.Close()

		dec := json.NewDecoder(resp.Body)
		for {
			var chunk chatResponse
			if err := dec.Decode(&chunk); err != nil {
				if err != io.EOF {
					yield(&llmv1.StreamChunk{Done: true, Error: util.Ptr(err.Error())}, nil)
				}
				return
			}
			if chunk.Message.Thinking != "" {
				if !yield(&llmv1.StreamChunk{
					Delta: &llmv1.StreamChunk_Thinking{Thinking: &threadv1.ThinkingContent{Text: chunk.Message.Thinking}},
				}, nil) {
					return
				}
			}
			if chunk.Message.Content != "" {
				if !yield(&llmv1.StreamChunk{
					Delta: &llmv1.StreamChunk_Text{Text: &threadv1.TextContent{Text: chunk.Message.Content}},
				}, nil) {
					return
				}
			}
			for _, tc := range chunk.Message.ToolCalls {
				argsStr := "{}"
				if len(tc.Function.Arguments) > 0 {
					argsStr = string(tc.Function.Arguments)
				}
				if !yield(&llmv1.StreamChunk{
					Delta: &llmv1.StreamChunk_ToolCall{ToolCall: &threadv1.ToolCallContent{
						Id:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: argsStr,
					}},
				}, nil) {
					return
				}
			}
			if chunk.Done {
				yield(&llmv1.StreamChunk{
					Done: true,
					Usage: &llmv1.Usage{
						PromptTokens:     int32(chunk.PromptEval),
						CompletionTokens: int32(chunk.EvalCount),
					},
				}, nil)
				return
			}
		}
	}
}

// --- Embedder ---

type embedder struct {
	model   string
	baseURL string
	client  *httpc.Client
}

// Embed routes both roles to the same /api/embed endpoint —
// ollama's embed API doesn't differentiate query/document at the
// request level. Any asymmetric behavior is up to the served
// model's own prompt template; the adapter passes texts through
// unmodified.
func (e *embedder) Embed(ctx context.Context, _ core.EmbedRole, texts []string) ([][]float32, error) {
	return e.embed(ctx, texts)
}

// embed loops over inputs because /api/embed accepts a single string
// at a time. Folding the per-text call into one helper keeps the
// public surface uniform (always [][]float32) while preserving the
// fail-on-first-error semantic of the all-or-nothing batch contract.
func (e *embedder) embed(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := e.embedOne(ctx, text)
		if err != nil {
			return nil, err
		}
		results[i] = vec
	}
	return results, nil
}

func (e *embedder) embedOne(ctx context.Context, text string) ([]float32, error) {
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
