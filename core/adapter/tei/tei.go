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
	"github.com/emontenegr/spidey/core/httpc/retry"
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
//
// QueryPrefix is text prepended to each input on EmbedQuery (and only there;
// EmbedDocument plain-encodes). Asymmetric instruction-tuned embedders like
// Qwen3-Embedding expect a query-side instruction baked into the input text
// itself, e.g. "Instruct: Given a follow-up message in a conversation, retrieve
// the prior message that contains the prerequisite information needed to answer
// it\nQuery: ". TEI's --default-prompt-name is server-wide and not per-request,
// but Qwen3's recipe doesn't need server-side prompt support — the instruction
// IS part of the query text. Empty QueryPrefix preserves the prior symmetric
// behavior for bge-m3-class models.
type Config struct {
	BaseURL     string // e.g., "http://localhost:8080"
	QueryPrefix string // optional prefix prepended to EmbedQuery inputs only
}

type provider struct {
	cfg    Config
	client *httpc.Client
}

// New creates a TEI provider. Supports Embedder (/embed) and Scorer
// (/rerank). Embedder serves dense embeddings from a bi-encoder model
// like bge-m3; Scorer serves query-vs-candidates relevance scores
// from a reranker model like bge-reranker-v2-m3.
func New(cfg Config) any {
	return &provider{
		cfg:    cfg,
		client: httpc.New(httpc.TimeoutTEI, nil),
	}
}


func (p *provider) Embedder(_ string) (core.Embedder, error) {
	return &embedder{
		baseURL:     p.cfg.BaseURL,
		client:      p.client,
		queryPrefix: p.cfg.QueryPrefix,
	}, nil
}

func (p *provider) Scorer(_ string) (core.Scorer, error) {
	return &scorer{
		baseURL: p.cfg.BaseURL,
		client:  p.client,
	}, nil
}

// --- Embedder ---

type embedder struct {
	baseURL     string
	client      *httpc.Client
	queryPrefix string
}

// TEI accepts `truncate: true` to automatically cut inputs to the
// model's max_position_embeddings. Without it, requests over the
// limit get rejected with a 413 or silently truncated depending on
// TEI version — both are bad modes. With it, TEI cuts at 8192 tokens
// (bge-m3 / bge-reranker-v2-m3 limit) deterministically. The chunker
// above this layer should keep inputs well under that ceiling; this
// is defense in depth.

type batchEmbedRequest struct {
	Inputs   []string `json:"inputs"`
	Truncate bool     `json:"truncate"`
}

// Embed dispatches on role. RoleQuery prepends Config.QueryPrefix
// to each input (when set) before calling /embed; RoleDocument
// plain-encodes. For symmetric models like bge-m3 leave QueryPrefix
// empty and both roles route identically. For asymmetric
// instruction-tuned models like Qwen3-Embedding, set QueryPrefix
// to the model's documented Instruct/Query prefix; the asymmetry
// then lives in the input text itself rather than in any
// server-side prompt config.
func (e *embedder) Embed(ctx context.Context, role core.EmbedRole, texts []string) ([][]float32, error) {
	if role == core.RoleQuery && e.queryPrefix != "" {
		prefixed := make([]string, len(texts))
		for i, t := range texts {
			prefixed[i] = e.queryPrefix + t
		}
		return e.embed(ctx, prefixed)
	}
	return e.embed(ctx, texts)
}

func (e *embedder) embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(batchEmbedRequest{Inputs: texts, Truncate: true})
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

// --- Scorer (reranker) ---

type scorer struct {
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
func (s *scorer) Score(ctx context.Context, query string, candidates []string) ([]float64, error) {
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
		if err := s.scoreOnce(ctx, query, slice, scores[offset:end]); err != nil {
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
// as a scorer error that would pause the whole round.
func (s *scorer) scoreOnce(ctx context.Context, query string, slice []string, out []float64) error {
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
		httpReq, err := http.NewRequest("POST", s.baseURL+"/rerank", bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")

		respBody, status, err := s.client.DoJSON(ctx, httpReq)
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

