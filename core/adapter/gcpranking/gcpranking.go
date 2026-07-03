// Package gcpranking adapts Google Vertex's hosted reranker — the Discovery
// Engine Ranking API — to grudge's Scorer role over Application Default
// Credentials. It is split from the vertex adapter deliberately: the ranker
// is a different product (discoveryengine, not aiplatform), a different wire
// (rank records, not Gemini generate), and a separate IAM enablement, so by
// the identity rule it is its own adapter even though it shares ADC auth.
//
// grudge's own eval names the reranker "the load-bearing layer" (top-1
// collapses without it), so this is the principled replacement for the
// self-hosted zerank/TEI scorer when running fully on Vertex — not the
// degraded nil-scorer path.
package gcpranking

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/core/adapter/internal/gcpauth"
	"github.com/elijahmontenegro/grudge/core/httpc/retry"
	"github.com/elijahmontenegro/grudge/core/internal/httpc"
)

// rankBatchSize caps records per :rank call. The Discovery Engine Ranking API
// bounds records per request; batching keeps the aligned output correct
// regardless of candidate count. RerankTopK=64 keeps the common path single-batch.
const rankBatchSize = 100

// defaultModel is the hosted semantic ranker used when no model is configured.
const defaultModel = "semantic-ranker-default-004"

type provider struct {
	project  string
	location string
	client   *httpc.Client
}

// New builds a gcpranking provider. Project resolves from Options["project"]
// / GOOGLE_CLOUD_PROJECT. Ranking API location defaults to "global"
// INDEPENDENTLY of the Gemini region (Ranking locations are global/us/eu, not
// the aiplatform region set) — read from Options["ranking_location"] to override.
func New(cfg core.ProviderConfig) (any, error) {
	project := gcpauth.Project(cfg.Option("project"))
	if project == "" {
		return nil, fmt.Errorf("gcpranking: GCP project required (set Options[\"project\"] or GOOGLE_CLOUD_PROJECT)")
	}
	location := cfg.Option("ranking_location")
	if location == "" {
		location = "global"
	}
	authFn, err := gcpauth.BearerAuthFn(context.Background())
	if err != nil {
		return nil, fmt.Errorf("gcpranking: ADC token source: %w", err)
	}
	return &provider{
		project:  project,
		location: location,
		client:   httpc.New(httpc.TimeoutDefault, authFn),
	}, nil
}

func (p *provider) Scorer(model string) (core.Scorer, error) {
	if model == "" {
		model = defaultModel
	}
	return &scorer{
		model:    model,
		project:  p.project,
		location: p.location,
		client:   p.client,
	}, nil
}

type scorer struct {
	model    string
	project  string
	location string
	client   *httpc.Client
}

type rankRecord struct {
	ID      string  `json:"id"`
	Content string  `json:"content"`
	Score   float64 `json:"score,omitempty"`
}

type rankRequest struct {
	Model                         string       `json:"model"`
	Query                         string       `json:"query"`
	Records                       []rankRecord `json:"records"`
	IgnoreRecordDetailsInResponse bool         `json:"ignoreRecordDetailsInResponse"`
}

type rankResponse struct {
	Records []rankRecord `json:"records"`
}

// Score ranks candidates against query and returns scores aligned to the
// input order: scores[i] is the score for candidates[i], regardless of the
// order the API returns records in (records carry back their input-index id).
func (s *scorer) Score(ctx context.Context, query string, candidates []string) ([]float64, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if query == "" {
		// Ranker requires a query; zero-fill gives callers a consistent
		// shape for "no relevance signal" (mirrors the tei scorer).
		return make([]float64, len(candidates)), nil
	}

	out := make([]float64, len(candidates))
	for offset := 0; offset < len(candidates); offset += rankBatchSize {
		end := min(offset+rankBatchSize, len(candidates))
		if err := s.rankBatch(ctx, query, candidates, offset, end, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *scorer) rankBatch(ctx context.Context, query string, candidates []string, offset, end int, out []float64) error {
	records := make([]rankRecord, 0, end-offset)
	for i := offset; i < end; i++ {
		records = append(records, rankRecord{ID: strconv.Itoa(i), Content: candidates[i]})
	}
	reqBody := rankRequest{Model: s.model, Query: query, Records: records}

	url := fmt.Sprintf(
		"https://discoveryengine.googleapis.com/v1/projects/%s/locations/%s/rankingConfigs/default_ranking_config:rank",
		s.project, s.location,
	)

	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	var resp rankResponse
	err = retry.Do(ctx, retry.LocalServicePolicy(), retry.LogRetryEvent("gcpranking rank"), func(ctx context.Context) error {
		httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		respBody, status, err := s.client.DoJSON(ctx, httpReq)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("%w: gcpranking returned %d: %s", core.ErrProviderUnavailable, status, respBody)
		}
		return json.Unmarshal(respBody, &resp)
	})
	if err != nil {
		return err
	}

	alignScores(resp.Records, out)
	return nil
}

// alignScores writes each returned record's score into out at the input index
// encoded in its id, so out[i] aligns to candidates[i] regardless of the
// order the Ranking API returns records in. Out-of-range/unparseable ids are
// skipped (defensive — the API echoes the ids we sent). This is the
// order-preservation contract of core.Scorer, isolated for testing.
func alignScores(records []rankRecord, out []float64) {
	for _, rec := range records {
		idx, err := strconv.Atoi(rec.ID)
		if err != nil || idx < 0 || idx >= len(out) {
			continue
		}
		out[idx] = rec.Score
	}
}
