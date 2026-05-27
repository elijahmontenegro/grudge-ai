package googleai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/internal/httpc"
	pb "github.com/emontenegr/spidey/proto/gen/go/spidey/v1"
)

const baseURL = "https://generativelanguage.googleapis.com/v1"

// Config for the Google AI (Gemini) provider.
type Config struct {
	APIKey string
}

type provider struct {
	cfg    Config
	client *httpc.Client
}

// New creates a Google AI provider.
func New(cfg Config) any {
	return &provider{
		cfg:    cfg,
		client: httpc.New(httpc.TimeoutDefault, nil),
	}
}


func (p *provider) Completer(model string) (core.Completer, error) {
	return &completer{
		model:        model,
		apiKey:       p.cfg.APIKey,
		client:       p.client,
		streamClient: httpc.NewStreaming(nil),
	}, nil
}

func (p *provider) Embedder(model string) (core.Embedder, error) {
	return &embedder{
		model:  model,
		apiKey: p.cfg.APIKey,
		client: p.client,
	}, nil
}

// --- Completer ---

type completer struct {
	model        string
	apiKey       string
	client       *httpc.Client
	streamClient *httpc.Client
}


func (c *completer) Complete(ctx context.Context, req *pb.CompletionRequest) (*pb.CompletionResponse, error) {
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", baseURL, c.model, c.apiKey)

	body, err := json.Marshal(toGenerateRequest(req))
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := c.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: googleai returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp generateResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	if len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("%w: googleai returned no candidates", core.ErrProviderUnavailable)
	}

	return &pb.CompletionResponse{
		Model: c.model,
		Message: &pb.LLMMessage{
			Role:    pb.Role_ROLE_ASSISTANT,
			Content: fromGeminiParts(resp.Candidates[0].Content.Parts),
		},
		Usage: &pb.Usage{
			PromptTokens:     resp.UsageMeta.PromptTokenCount,
			CompletionTokens: resp.UsageMeta.CandidatesTokenCount,
		},
	}, nil
}

func (c *completer) Stream(ctx context.Context, req *pb.CompletionRequest) iter.Seq2[*pb.StreamChunk, error] {
	return func(yield func(*pb.StreamChunk, error) bool) {
		url := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse&key=%s", baseURL, c.model, c.apiKey)

		body, err := json.Marshal(toGenerateRequest(req))
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
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
			resp.Body.Close()
			yield(nil, &httpc.StatusError{Provider: "googleai", StatusCode: resp.StatusCode})
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

			var chunk generateResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				yield(&pb.StreamChunk{Done: true, Error: ptr(err.Error())}, nil)
				return
			}

			if len(chunk.Candidates) > 0 {
				for _, p := range chunk.Candidates[0].Content.Parts {
					if p.Text != "" {
						if !yield(&pb.StreamChunk{
							Delta: &pb.StreamChunk_Text{Text: &pb.TextContent{Text: p.Text}},
						}, nil) {
							return
						}
					}
				}
			}

			if chunk.UsageMeta.PromptTokenCount > 0 {
				yield(&pb.StreamChunk{
					Done: true,
					Usage: &pb.Usage{
						PromptTokens:     chunk.UsageMeta.PromptTokenCount,
						CompletionTokens: chunk.UsageMeta.CandidatesTokenCount,
					},
				}, nil)
				return
			}
		}
		if err := scanner.Err(); err != nil {
			yield(&pb.StreamChunk{Done: true, Error: ptr(err.Error())}, nil)
		}
	}
}

// --- Embedder ---

type embedder struct {
	model  string
	apiKey string
	client *httpc.Client
}


// Embed routes both roles to the same batch endpoint — googleai's
// embedContent API doesn't differentiate query/document at the
// request level (the model itself may; pass it via the task_type
// field if surfaced in future).
func (e *embedder) Embed(ctx context.Context, _ core.EmbedRole, texts []string) ([][]float32, error) {
	return e.embed(ctx, texts)
}

func (e *embedder) embed(ctx context.Context, texts []string) ([][]float32, error) {
	url := fmt.Sprintf("%s/models/%s:batchEmbedContents?key=%s", baseURL, e.model, e.apiKey)

	reqs := make([]embedContentRequest, len(texts))
	for i, t := range texts {
		reqs[i] = embedContentRequest{
			Content: content{Parts: []part{{Text: t}}},
		}
	}

	body, err := json.Marshal(batchEmbedRequest{Requests: reqs})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := e.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: googleai returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp batchEmbedResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	if len(resp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("%w: googleai returned %d embeddings for %d inputs", core.ErrProviderUnavailable, len(resp.Embeddings), len(texts))
	}

	results := make([][]float32, len(resp.Embeddings))
	for i, e := range resp.Embeddings {
		results[i] = e.Values
	}
	return results, nil
}

