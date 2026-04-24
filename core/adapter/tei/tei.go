package tei

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/internal/httpc"
	"github.com/emontenegr/spidey/core/resilience"
)

// teiRetryPolicy is the retry shape for a single TEI HTTP call.
// Tighter than the default because TEI is a local service — a real
// wedge won't fix itself within seconds, but a transient slow batch
// or a just-restarted container (healthcheck kicked in) will. The
// 120s per-attempt ceiling (via httpc.TimeoutTEI) × 3 attempts gives
// a ~6-minute window that accommodates one full restart-and-warmup
// cycle; beyond that the error propagates and RRC pauses the round
// per spec.
func teiRetryPolicy() resilience.Policy {
	return resilience.Policy{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Second,
		MaxDelay:    10 * time.Second,
		Multiplier:  3.0,
		Jitter:      0.25,
	}
}

// Config for the TEI (Hugging Face Text Embeddings Inference) provider.
type Config struct {
	BaseURL string // e.g., "http://localhost:8080"
}

type provider struct {
	cfg    Config
	client *httpc.Client
}

// New creates a TEI provider. Supports Embedder (/embed) and Classifier
// (/rerank). Embedder serves dense embeddings from a bi-encoder model
// like bge-m3; Classifier serves query-vs-candidates relevance scores
// from a reranker model like bge-reranker-v2-m3.
func New(cfg Config) core.Provider {
	return &provider{
		cfg:    cfg,
		client: httpc.New(httpc.TimeoutTEI, nil),
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

// TEI accepts `truncate: true` to automatically cut inputs to the
// model's max_position_embeddings. Without it, requests over the
// limit get rejected with a 413 or silently truncated depending on
// TEI version — both are bad modes. With it, TEI cuts at 8192 tokens
// (bge-m3 / bge-reranker-v2-m3 limit) deterministically. The chunker
// above this layer should keep inputs well under that ceiling; this
// is defense in depth.

type embedRequest struct {
	Inputs   string `json:"inputs"`
	Truncate bool   `json:"truncate"`
}

type batchEmbedRequest struct {
	Inputs   []string `json:"inputs"`
	Truncate bool     `json:"truncate"`
}

func (e *embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Inputs: text, Truncate: true})
	if err != nil {
		return nil, err
	}

	var resp [][]float32
	err = resilience.Do(ctx, teiRetryPolicy(), logRetryEvent("tei embed"), func(ctx context.Context) error {
		httpReq, err := http.NewRequest("POST", e.baseURL+"/embed", bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")

		respBody, status, err := e.client.DoJSON(ctx, httpReq)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("%w: tei returned %d: %s", core.ErrProviderUnavailable, status, respBody)
		}
		return json.Unmarshal(respBody, &resp)
	})
	if err != nil {
		return nil, err
	}
	if len(resp) == 0 {
		return nil, fmt.Errorf("%w: tei returned empty embedding", core.ErrProviderUnavailable)
	}
	return resp[0], nil
}

func (e *embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(batchEmbedRequest{Inputs: texts, Truncate: true})
	if err != nil {
		return nil, err
	}

	var resp [][]float32
	err = resilience.Do(ctx, teiRetryPolicy(), logRetryEvent("tei embed-batch"), func(ctx context.Context) error {
		httpReq, err := http.NewRequest("POST", e.baseURL+"/embed", bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")

		respBody, status, err := e.client.DoJSON(ctx, httpReq)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("%w: tei returned %d: %s", core.ErrProviderUnavailable, status, respBody)
		}
		return json.Unmarshal(respBody, &resp)
	})
	if err != nil {
		return nil, err
	}
	if len(resp) != len(texts) {
		return nil, fmt.Errorf("%w: tei returned %d embeddings for %d inputs", core.ErrProviderUnavailable, len(resp), len(texts))
	}
	return resp, nil
}

// logRetryEvent produces a resilience.Do event handler that surfaces
// TEI retries into the server log. The standard [Retry] prefix lets
// operators grep a single stream for transient-infrastructure events
// across all providers. Only retry events (Err != nil, !Final) and
// terminal failure events are logged — successes are silent to keep
// log volume sane under healthy load.
func logRetryEvent(op string) func(resilience.Event) {
	return func(e resilience.Event) {
		if e.Err == nil {
			return
		}
		if e.Final {
			log.Printf("[Retry] %s exhausted after attempt %d/%d: %v",
				op, e.Attempt, e.MaxAttempts, e.Err)
			return
		}
		log.Printf("[Retry] %s attempt %d/%d failed: %v — next attempt in %s",
			op, e.Attempt, e.MaxAttempts, e.Err, e.NextDelay.Round(100*time.Millisecond))
	}
}

// --- Classifier (reranker) ---

type classifier struct {
	baseURL string
	client  *httpc.Client
}

// TEI /rerank request: one query, many texts. The reranker returns
// scores indexed by candidate position (may not be in input order,
// so we map back via the response's `index` field).
type rerankRequest struct {
	Query     string   `json:"query"`
	Texts     []string `json:"texts"`
	RawScores bool     `json:"raw_scores"`
	Truncate  bool     `json:"truncate"`
}

type rerankHit struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// rerankBatchSize caps candidates per /rerank call. TEI's default
// `max_client_batch_size` is 32; exceeding it returns a 422. We
// split larger requests into batches transparently so callers
// don't have to think about server-side limits.
const rerankBatchSize = 32

// Rerank calls TEI's /rerank endpoint, batching candidates when
// necessary so a large candidate list doesn't exceed TEI's
// `max_client_batch_size`. Response scores are aligned to the input
// order: the caller treats `scores[i]` as the score for `candidates[i]`
// even when the request was split across multiple HTTP calls
// internally. Empty candidates returns an empty slice without a
// network call.
func (c *classifier) Rerank(ctx context.Context, query string, candidates []string) ([]float64, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if query == "" {
		// Reranker models require a non-empty query. Zero-out gives
		// callers a consistent shape for "no relevance signal" without
		// forcing them to special-case the empty-query path.
		return make([]float64, len(candidates)), nil
	}

	scores := make([]float64, len(candidates))
	for offset := 0; offset < len(candidates); offset += rerankBatchSize {
		end := offset + rerankBatchSize
		if end > len(candidates) {
			end = len(candidates)
		}
		slice := candidates[offset:end]
		if err := c.rerankOnce(ctx, query, slice, scores[offset:end]); err != nil {
			return nil, err
		}
	}
	return scores, nil
}

// rerankOnce is the single-batch /rerank call. Fills `out` in order
// aligned to `slice`. Caller ensures len(slice) ≤ rerankBatchSize.
// Wrapped in resilience.Do so a transient TEI slowness or a
// restart-in-progress container (triggered by the docker-compose
// healthcheck's canary /rerank probe) is retried rather than propagated
// as a classifier error that would pause the whole round.
func (c *classifier) rerankOnce(ctx context.Context, query string, slice []string, out []float64) error {
	body, err := json.Marshal(rerankRequest{
		Query:     query,
		Texts:     slice,
		RawScores: false,
		Truncate:  true,
	})
	if err != nil {
		return err
	}
	return resilience.Do(ctx, teiRetryPolicy(), logRetryEvent("tei rerank"), func(ctx context.Context) error {
		httpReq, err := http.NewRequest("POST", c.baseURL+"/rerank", bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")

		respBody, status, err := c.client.DoJSON(ctx, httpReq)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("%w: tei returned %d: %s", core.ErrProviderUnavailable, status, respBody)
		}
		var hits []rerankHit
		if err := json.Unmarshal(respBody, &hits); err != nil {
			return err
		}
		for _, h := range hits {
			if h.Index < 0 || h.Index >= len(slice) {
				continue
			}
			out[h.Index] = h.Score
		}
		return nil
	})
}
