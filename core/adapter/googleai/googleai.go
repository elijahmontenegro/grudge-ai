package googleai

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
func New(cfg Config) core.Provider {
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

func (p *provider) Classifier(_ string) (core.Classifier, error) {
	return nil, core.ErrUnsupported
}


// --- Completer ---

type completer struct {
	model        string
	apiKey       string
	client       *httpc.Client
	streamClient *httpc.Client
}

type generateRequest struct {
	Contents         []content        `json:"contents"`
	SystemInstruct   *content         `json:"systemInstruction,omitempty"`
	GenerationConfig *generationConfig `json:"generationConfig,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text string `json:"text,omitempty"`
}

type generationConfig struct {
	MaxOutputTokens *int32   `json:"maxOutputTokens,omitempty"`
	Temperature     *float32 `json:"temperature,omitempty"`
	TopP            *float32 `json:"topP,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

type generateResponse struct {
	Candidates []candidate  `json:"candidates"`
	UsageMeta  usageMetadata `json:"usageMetadata"`
}

type candidate struct {
	Content content `json:"content"`
}

type usageMetadata struct {
	PromptTokenCount     int32 `json:"promptTokenCount"`
	CandidatesTokenCount int32 `json:"candidatesTokenCount"`
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

func (c *completer) Stream(ctx context.Context, req *pb.CompletionRequest) (<-chan *pb.StreamChunk, error) {
	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse&key=%s", baseURL, c.model, c.apiKey)

	body, err := json.Marshal(toGenerateRequest(req))
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
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
		return nil, &httpc.StatusError{Provider: "googleai", StatusCode: resp.StatusCode}
	}

	ch := make(chan *pb.StreamChunk)
	go func() {
		defer close(ch)
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
				ch <- &pb.StreamChunk{Done: true, Error: ptr(err.Error())}
				return
			}

			if len(chunk.Candidates) > 0 {
				for _, p := range chunk.Candidates[0].Content.Parts {
					if p.Text != "" {
						ch <- &pb.StreamChunk{
							Delta: &pb.StreamChunk_Text{Text: &pb.TextContent{Text: p.Text}},
						}
					}
				}
			}

			if chunk.UsageMeta.PromptTokenCount > 0 {
				ch <- &pb.StreamChunk{
					Done: true,
					Usage: &pb.Usage{
						PromptTokens:     chunk.UsageMeta.PromptTokenCount,
						CompletionTokens: chunk.UsageMeta.CandidatesTokenCount,
					},
				}
				return
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
	model  string
	apiKey string
	client *httpc.Client
}

type embedContentRequest struct {
	Content content `json:"content"`
}

type embedContentResponse struct {
	Embedding embedValues `json:"embedding"`
}

type embedValues struct {
	Values []float32 `json:"values"`
}

type batchEmbedRequest struct {
	Requests []embedContentRequest `json:"requests"`
}

type batchEmbedResponse struct {
	Embeddings []embedValues `json:"embeddings"`
}

func (e *embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	url := fmt.Sprintf("%s/models/%s:embedContent?key=%s", baseURL, e.model, e.apiKey)

	body, err := json.Marshal(embedContentRequest{
		Content: content{Parts: []part{{Text: text}}},
	})
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

	var resp embedContentResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	return resp.Embedding.Values, nil
}

func (e *embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
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

// --- helpers ---

func toGenerateRequest(req *pb.CompletionRequest) generateRequest {
	var sysContent *content
	var contents []content

	for _, m := range req.Messages {
		if m.Role == pb.Role_ROLE_SYSTEM {
			text := textFromBlocks(m.Content)
			sysContent = &content{Parts: []part{{Text: text}}}
			continue
		}
		role := "user"
		if m.Role == pb.Role_ROLE_ASSISTANT {
			role = "model"
		}
		parts := make([]part, 0, len(m.Content))
		for _, b := range m.Content {
			if t := b.GetText(); t != nil {
				parts = append(parts, part{Text: t.Text})
			}
		}
		contents = append(contents, content{Role: role, Parts: parts})
	}

	var genCfg *generationConfig
	if req.MaxTokens != nil || req.Temperature != nil || req.TopP != nil || len(req.Stop) > 0 {
		genCfg = &generationConfig{
			MaxOutputTokens: req.MaxTokens,
			Temperature:     req.Temperature,
			TopP:            req.TopP,
			StopSequences:   req.Stop,
		}
	}

	return generateRequest{
		Contents:         contents,
		SystemInstruct:   sysContent,
		GenerationConfig: genCfg,
	}
}

func fromGeminiParts(parts []part) []*pb.ContentBlock {
	blocks := make([]*pb.ContentBlock, 0, len(parts))
	for _, p := range parts {
		if p.Text != "" {
			blocks = append(blocks, &pb.ContentBlock{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: p.Text}}})
		}
	}
	return blocks
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

func ptr(s string) *string { return &s }
