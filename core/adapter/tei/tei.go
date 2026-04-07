package tei

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/internal/httpc"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// Config for the TEI (Hugging Face Text Embeddings Inference) provider.
type Config struct {
	BaseURL string // e.g., "http://localhost:8080"
}

type provider struct {
	cfg    Config
	client *httpc.Client
}

// New creates a TEI provider. Supports Embedder (/embed) and Classifier (/predict).
func New(cfg Config) core.Provider {
	return &provider{
		cfg:    cfg,
		client: httpc.New(httpc.TimeoutDefault, nil),
	}
}

func (p *provider) ID() string { return "tei" }

func (p *provider) Completer(_ string) (core.Completer, error) {
	return nil, core.ErrUnsupported
}

func (p *provider) Embedder(_ string) (core.Embedder, error) {
	return &embedder{
		baseURL: p.cfg.BaseURL,
		client:  p.client,
	}, nil
}

func (p *provider) Classifier(_ string) (core.Classifier, error) {
	return &classifier{
		baseURL: p.cfg.BaseURL,
		client:  p.client,
	}, nil
}

func (p *provider) Close() error { return nil }

// --- Embedder ---

type embedder struct {
	baseURL string
	client  *httpc.Client
}

type embedRequest struct {
	Inputs string `json:"inputs"`
}

type batchEmbedRequest struct {
	Inputs []string `json:"inputs"`
}

func (e *embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Inputs: text})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", e.baseURL+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := e.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: tei returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp [][]float32
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	if len(resp) == 0 {
		return nil, fmt.Errorf("%w: tei returned empty embedding", core.ErrProviderUnavailable)
	}
	return resp[0], nil
}

func (e *embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(batchEmbedRequest{Inputs: texts})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", e.baseURL+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := e.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: tei returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp [][]float32
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	if len(resp) != len(texts) {
		return nil, fmt.Errorf("%w: tei returned %d embeddings for %d inputs", core.ErrProviderUnavailable, len(resp), len(texts))
	}
	return resp, nil
}

// --- Classifier ---

type classifier struct {
	baseURL string
	client  *httpc.Client
}

type predictRequest struct {
	Inputs string `json:"inputs"`
	TextPair string `json:"text_pair"`
}

type predictResponse [][]predictLabel

type predictLabel struct {
	Label string  `json:"label"`
	Score float32 `json:"score"`
}

func (c *classifier) Classify(ctx context.Context, req *pb.ClassifyRequest) (*pb.ClassifyResponse, error) {
	body, err := json.Marshal(predictRequest{
		Inputs:   req.TextA,
		TextPair: req.TextB,
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/predict", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := c.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: tei returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp predictResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	if len(resp) == 0 {
		return nil, fmt.Errorf("%w: tei returned empty prediction", core.ErrProviderUnavailable)
	}

	labels := make([]*pb.ClassLabel, len(resp[0]))
	for i, l := range resp[0] {
		labels[i] = &pb.ClassLabel{Name: l.Label, Probability: l.Score}
	}
	return &pb.ClassifyResponse{Labels: labels}, nil
}

func (c *classifier) ClassifyBatch(ctx context.Context, req *pb.BatchClassifyRequest) (*pb.BatchClassifyResponse, error) {
	results := make([]*pb.ClassifyResponse, len(req.Pairs))
	for i, pair := range req.Pairs {
		resp, err := c.Classify(ctx, pair)
		if err != nil {
			return nil, err
		}
		results[i] = resp
	}
	return &pb.BatchClassifyResponse{Results: results}, nil
}
