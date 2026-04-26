package tei

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/internal/httpc"
	"github.com/emontenegr/spidey/core/retry"
)

// teiRetryPolicy is the retry shape for a single TEI HTTP call.
// Tighter than the default because TEI is a local service — a real
// wedge won't fix itself within seconds, but a transient slow batch
// or a just-restarted container (healthcheck kicked in) will. The
// 120s per-attempt ceiling (via httpc.TimeoutTEI) × 3 attempts gives
// a ~6-minute window that accommodates one full restart-and-warmup
// cycle; beyond that the error propagates and RRC pauses the round
// per spec.
func teiRetryPolicy() retry.Policy {
	return retry.Policy{
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

// NewEntailer wires an NLI entailer to a TEI /predict endpoint.
// Returns nil on empty URL so callers can conditionally pass through
// "not configured" without a nil-check on the struct.
func NewEntailer(baseURL string) core.Entailer {
	if baseURL == "" {
		return nil
	}
	return &entailer{
		baseURL: baseURL,
		client:  httpc.New(httpc.TimeoutTEI, nil),
	}
}


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
	err = retry.Do(ctx, teiRetryPolicy(), logRetryEvent("tei embed"), func(ctx context.Context) error {
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
	err = retry.Do(ctx, teiRetryPolicy(), logRetryEvent("tei embed-batch"), func(ctx context.Context) error {
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

// logRetryEvent produces a retry.Do event handler that surfaces
// TEI retries into the server log. The standard [Retry] prefix lets
// operators grep a single stream for transient-infrastructure events
// across all providers. Only retry events (Err != nil, !Final) and
// terminal failure events are logged — successes are silent to keep
// log volume sane under healthy load.
func logRetryEvent(op string) func(retry.Event) {
	return func(e retry.Event) {
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
func (c *classifier) Score(ctx context.Context, query string, candidates []string) ([]float64, error) {
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
		if err := c.scoreOnce(ctx, query, slice, scores[offset:end]); err != nil {
			return nil, err
		}
	}
	return scores, nil
}

// rerankOnce is the single-batch /rerank call. Fills `out` in order
// aligned to `slice`. Caller ensures len(slice) ≤ rerankBatchSize.
// Wrapped in retry.Do so a transient TEI slowness or a
// restart-in-progress container (triggered by the docker-compose
// healthcheck's canary /rerank probe) is retried rather than propagated
// as a classifier error that would pause the whole round.
func (c *classifier) scoreOnce(ctx context.Context, query string, slice []string, out []float64) error {
	body, err := json.Marshal(rerankRequest{
		Query:     query,
		Texts:     slice,
		RawScores: false,
		Truncate:  true,
	})
	if err != nil {
		return err
	}
	return retry.Do(ctx, teiRetryPolicy(), logRetryEvent("tei rerank"), func(ctx context.Context) error {
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

// --- Entailer (NLI classification) ---
//
// Layered on top of the reranker per spec — composite NLI stage. TEI
// serves sequence-classification models (DeBERTa-MNLI and similar)
// behind the /predict endpoint. The shape accepts paired inputs
// (premise, hypothesis) and returns per-class scores; we extract the
// "entailment" class probability and return it as the entailment
// signal per (premise, hypothesis) pair.
//
// Model-agnostic class-name matching: MNLI-trained models commonly
// label classes "entailment" / "neutral" / "contradiction" or the
// uppercase variants. We match by name rather than index so a model
// with classes in a different order still works. Missing the label
// returns 0 — treating "no entailment evidence" as zero rather than
// falling back to a synthetic score that would silently bias fusion.

type entailer struct {
	baseURL string
	client  *httpc.Client
}

type predictRequest struct {
	Inputs    [][]string `json:"inputs"`    // pairs of (premise, hypothesis)
	Truncate  bool       `json:"truncate"`
	RawScores bool       `json:"raw_scores"`
}

type predictHit struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
}

// predictBatchSize caps pairs per /predict call. TEI's default
// max_client_batch_size is 32; batching mirrors the reranker path so
// large candidate lists don't exceed the server-side limit.
const predictBatchSize = 32

// Entail calls TEI's /predict for each (premise, hypothesis) pair and
// returns the entailment-class score aligned to the input order of
// hypotheses. Empty hypotheses returns an empty slice without a
// network call.
func (en *entailer) Score(ctx context.Context, premise string, hypotheses []string) ([]float64, error) {
	if len(hypotheses) == 0 {
		return nil, nil
	}
	if premise == "" {
		// NLI requires a non-empty premise; without one, fusion can't
		// contribute. Zero-out matches the reranker's empty-query
		// policy for shape parity.
		return make([]float64, len(hypotheses)), nil
	}
	scores := make([]float64, len(hypotheses))
	for offset := 0; offset < len(hypotheses); offset += predictBatchSize {
		end := offset + predictBatchSize
		if end > len(hypotheses) {
			end = len(hypotheses)
		}
		slice := hypotheses[offset:end]
		if err := en.scoreOnce(ctx, premise, slice, scores[offset:end]); err != nil {
			return nil, err
		}
	}
	return scores, nil
}

func (en *entailer) scoreOnce(ctx context.Context, premise string, slice []string, out []float64) error {
	pairs := make([][]string, len(slice))
	for i, h := range slice {
		pairs[i] = []string{premise, h}
	}
	body, err := json.Marshal(predictRequest{
		Inputs:    pairs,
		Truncate:  true,
		RawScores: false,
	})
	if err != nil {
		return err
	}
	return retry.Do(ctx, teiRetryPolicy(), logRetryEvent("tei predict"), func(ctx context.Context) error {
		httpReq, err := http.NewRequest("POST", en.baseURL+"/predict", bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")

		respBody, status, err := en.client.DoJSON(ctx, httpReq)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("%w: tei returned %d: %s", core.ErrProviderUnavailable, status, respBody)
		}
		// TEI returns [][]predictHit for batched predict — one hit
		// list per input pair, each list carrying one entry per
		// class. Single-pair responses can also come back as
		// []predictHit; the decoder handles both shapes.
		var batch [][]predictHit
		if err := json.Unmarshal(respBody, &batch); err != nil {
			var single []predictHit
			if err2 := json.Unmarshal(respBody, &single); err2 != nil {
				return err
			}
			batch = [][]predictHit{single}
		}
		for i := range slice {
			if i >= len(batch) {
				break
			}
			out[i] = entailmentScore(batch[i])
		}
		return nil
	})
}

// entailmentScore picks the entailment-class score from a predict
// response. Tolerant to label case and to models that label the
// class "ENTAILMENT" / "entailment" / "entail". Returns 0 when no
// matching class is present.
func entailmentScore(hits []predictHit) float64 {
	for _, h := range hits {
		lbl := strings.ToLower(h.Label)
		if lbl == "entailment" || lbl == "entail" {
			return h.Score
		}
	}
	return 0
}
