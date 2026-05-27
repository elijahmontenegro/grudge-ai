package zerank

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emontenegr/spidey/core"
)

// TestScoreExtractsBinaryLogitAndAppliesSigmoid verifies the corrected
// scoring recipe: when both "Yes" and "No" tokens are present in
// top_logprobs, the score is sigmoid((yes_logprob - no_logprob) / 5).
// This recovers the binary log-odds (Yes vs No) from the post-softmax
// logprobs the OpenAI API exposes — the faithful reconstruction of
// zerank's model-card formula `sigmoid(yes_logit / 5)`, whose
// "yes_logit" is the PRE-softmax score not directly accessible from
// the API. Feeding the bare yes_logprob (ln(P_yes) ≤ 0) into the
// model-card formula caps scores at 0.5 — incorrect.
func TestScoreExtractsBinaryLogitAndAppliesSigmoid(t *testing.T) {
	const yesLogprob = -1.2 // arbitrary
	const noLogprob = -3.0
	// binary_logit = ln(P_yes / P_no) = logprob_yes - logprob_no
	binaryLogit := yesLogprob - noLogprob
	expected := 1.0 / (1.0 + math.Exp(-binaryLogit/5.0))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "wrong path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		var req completionRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if !req.Logprobs || req.TopLogprobs <= 0 {
			t.Errorf("request must request logprobs: logprobs=%v top=%d",
				req.Logprobs, req.TopLogprobs)
		}
		if req.MaxTokens != 1 {
			t.Errorf("max_tokens should be 1, got %d", req.MaxTokens)
		}
		if len(req.Messages) != 2 ||
			req.Messages[0].Role != "system" ||
			req.Messages[1].Role != "user" {
			t.Errorf("messages should be system+user, got %+v", req.Messages)
		}

		resp := completionResponse{
			Choices: []struct {
				Logprobs tokenChoiceLogprobs `json:"logprobs"`
			}{{
				Logprobs: tokenChoiceLogprobs{
					Content: []struct {
						Token       string         `json:"token"`
						Logprob     float64        `json:"logprob"`
						TopLogprobs []tokenLogprob `json:"top_logprobs"`
					}{{
						Token:   "Yes",
						Logprob: yesLogprob,
						TopLogprobs: []tokenLogprob{
							{Token: "Yes", Logprob: yesLogprob},
							{Token: "No", Logprob: -3.0},
						},
					}},
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL, Model: "zeroentropy/zerank-1-small"}).(core.ScorerProvider)
	cls, err := p.Scorer("zeroentropy/zerank-1-small")
	if err != nil {
		t.Fatalf("Scorer: %v", err)
	}

	scores, err := cls.Score(context.Background(), "query", []string{"doc"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(scores) != 1 {
		t.Fatalf("got %d scores, want 1", len(scores))
	}
	if math.Abs(scores[0]-expected) > 1e-6 {
		t.Errorf("score mismatch: got %v, want %v", scores[0], expected)
	}
}

// TestScoreMatchesYesTokenWithLeadingSpace verifies that the BPE
// tokenizer's space-prefixed " Yes" still matches.
func TestScoreMatchesYesTokenWithLeadingSpace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(completionResponse{
			Choices: []struct {
				Logprobs tokenChoiceLogprobs `json:"logprobs"`
			}{{
				Logprobs: tokenChoiceLogprobs{
					Content: []struct {
						Token       string         `json:"token"`
						Logprob     float64        `json:"logprob"`
						TopLogprobs []tokenLogprob `json:"top_logprobs"`
					}{{
						Token: " Yes",
						TopLogprobs: []tokenLogprob{
							{Token: " Yes", Logprob: 0.0}, // sigmoid(0/5) = 0.5
						},
					}},
				},
			}},
		})
	}))
	defer srv.Close()

	cls, _ := New(Config{BaseURL: srv.URL}).(core.ScorerProvider).Scorer("zerank-1-small")
	scores, err := cls.Score(context.Background(), "q", []string{"d"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if math.Abs(scores[0]-0.5) > 1e-6 {
		t.Errorf("leading-space Yes should still score: got %v want 0.5", scores[0])
	}
}

// TestScoreReturnsZeroWhenYesAbsent verifies that when "Yes" isn't in
// top_logprobs, the score is 0 (model is confidently negative).
func TestScoreReturnsZeroWhenYesAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(completionResponse{
			Choices: []struct {
				Logprobs tokenChoiceLogprobs `json:"logprobs"`
			}{{
				Logprobs: tokenChoiceLogprobs{
					Content: []struct {
						Token       string         `json:"token"`
						Logprob     float64        `json:"logprob"`
						TopLogprobs []tokenLogprob `json:"top_logprobs"`
					}{{
						Token: "No",
						TopLogprobs: []tokenLogprob{
							{Token: "No", Logprob: -0.1},
							{Token: "no", Logprob: -2.0},
						},
					}},
				},
			}},
		})
	}))
	defer srv.Close()

	cls, _ := New(Config{BaseURL: srv.URL}).(core.ScorerProvider).Scorer("zerank-1-small")
	scores, err := cls.Score(context.Background(), "q", []string{"d"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if scores[0] != 0 {
		t.Errorf("absent Yes should score 0, got %v", scores[0])
	}
}

func TestScoreScoresMultipleCandidates(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(completionResponse{
			Choices: []struct {
				Logprobs tokenChoiceLogprobs `json:"logprobs"`
			}{{
				Logprobs: tokenChoiceLogprobs{
					Content: []struct {
						Token       string         `json:"token"`
						Logprob     float64        `json:"logprob"`
						TopLogprobs []tokenLogprob `json:"top_logprobs"`
					}{{
						Token: "Yes",
						TopLogprobs: []tokenLogprob{
							{Token: "Yes", Logprob: 0.0},
						},
					}},
				},
			}},
		})
	}))
	defer srv.Close()

	cls, _ := New(Config{BaseURL: srv.URL}).(core.ScorerProvider).Scorer("zerank-1-small")
	scores, err := cls.Score(context.Background(), "q", []string{"d1", "d2", "d3"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(scores) != 3 {
		t.Fatalf("got %d scores, want 3", len(scores))
	}
	if calls != 3 {
		t.Errorf("expected 3 vLLM calls, got %d", calls)
	}
}

// TestProviderImplementsScorerOnly verifies the adapter's role
// surface: zerank provides only the ScorerProvider role.
// Type-asserting against CompleterProvider or EmbedderProvider
// fails — there are no stub methods for those.
func TestProviderImplementsScorerOnly(t *testing.T) {
	p := New(Config{BaseURL: "http://localhost"})
	if _, ok := p.(core.ScorerProvider); !ok {
		t.Error("zerank should be a ScorerProvider")
	}
	if _, ok := p.(core.CompleterProvider); ok {
		t.Error("zerank should not be a CompleterProvider")
	}
	if _, ok := p.(core.EmbedderProvider); ok {
		t.Error("zerank should not be an EmbedderProvider")
	}
}
