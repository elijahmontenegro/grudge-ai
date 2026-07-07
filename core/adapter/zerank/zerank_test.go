package zerank

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/elijahmontenegro/grudge/core"
)

// classifyReply builds a /classify response body with one raw logit per input,
// mirroring vLLM's single-class (classifier_from_token ["Yes"]) score head.
func classifyReply(logits ...float64) classifyResponse {
	var resp classifyResponse
	resp.Data = make([]struct {
		Probs []float64 `json:"probs"`
	}, len(logits))
	for i, l := range logits {
		resp.Data[i].Probs = []float64{l}
	}
	return resp
}

// TestScoreAppliesSigmoidOverFive verifies the recipe: the raw "Yes" logit from
// /classify is mapped with sigmoid(logit / 5).
func TestScoreAppliesSigmoidOverFive(t *testing.T) {
	const logit = 6.02 // an in-distribution "relevant" logit
	want := 1.0 / (1.0 + math.Exp(-logit/scoreTemperature))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/classify" {
			http.Error(w, "wrong path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		var req classifyRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if req.UseActivation {
			t.Error("use_activation must be false so the raw logit is returned")
		}
		if len(req.Input) != 1 {
			t.Fatalf("want 1 input, got %d", len(req.Input))
		}
		// The input must be the chat-formatted prompt: query as system, document
		// as user, ending at the assistant generation position.
		in := req.Input[0]
		for _, want := range []string{
			"<|im_start|>system\nthe query<|im_end|>",
			"<|im_start|>user\nthe doc<|im_end|>",
			"<|im_start|>assistant\n",
		} {
			if !strings.Contains(in, want) {
				t.Errorf("prompt missing %q; got %q", want, in)
			}
		}
		if !strings.HasSuffix(in, "<|im_start|>assistant\n") {
			t.Errorf("prompt must end at the assistant generation position; got %q", in)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(classifyReply(logit))
	}))
	defer srv.Close()

	cls, err := New(Config{BaseURL: srv.URL, Model: "zeroentropy/zerank-1-small"}).(core.ScorerProvider).Scorer("")
	if err != nil {
		t.Fatalf("Scorer: %v", err)
	}
	scores, err := cls.Score(context.Background(), "the query", []string{"the doc"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(scores) != 1 {
		t.Fatalf("got %d scores, want 1", len(scores))
	}
	if math.Abs(scores[0]-want) > 1e-9 {
		t.Errorf("score = %v, want %v", scores[0], want)
	}
}

// TestScoreBatchesAllCandidatesInOneCall verifies every candidate is scored in a
// single /classify call and the per-candidate logits map through in order —
// including a strongly-negative distractor logit crushing to near zero.
func TestScoreBatchesAllCandidatesInOneCall(t *testing.T) {
	var calls int
	logits := []float64{6.02, -12.82, 1.80}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req classifyRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if len(req.Input) != len(logits) {
			t.Errorf("want %d inputs in one call, got %d", len(logits), len(req.Input))
		}
		_ = json.NewEncoder(w).Encode(classifyReply(logits...))
	}))
	defer srv.Close()

	cls, _ := New(Config{BaseURL: srv.URL}).(core.ScorerProvider).Scorer("")
	scores, err := cls.Score(context.Background(), "q", []string{"d1", "d2", "d3"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if calls != 1 {
		t.Errorf("want 1 batched vLLM call, got %d", calls)
	}
	if len(scores) != 3 {
		t.Fatalf("got %d scores, want 3", len(scores))
	}
	for i, l := range logits {
		want := 1.0 / (1.0 + math.Exp(-l/scoreTemperature))
		if math.Abs(scores[i]-want) > 1e-9 {
			t.Errorf("score[%d] = %v, want %v", i, scores[i], want)
		}
	}
	if scores[1] > 0.1 {
		t.Errorf("strongly-negative distractor should crush to ~0, got %v", scores[1])
	}
}

// TestScoreErrorsOnCountMismatch: a score-per-candidate contract violation must
// fail loudly, not silently misalign scores to the wrong documents.
func TestScoreErrorsOnCountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(classifyReply(1.0)) // one logit for two candidates
	}))
	defer srv.Close()

	cls, _ := New(Config{BaseURL: srv.URL}).(core.ScorerProvider).Scorer("")
	if _, err := cls.Score(context.Background(), "q", []string{"d1", "d2"}); err == nil {
		t.Fatal("count mismatch must error")
	}
}

// TestScoreErrorsOnEmptyProbs: a 200 with no logit is a serving fault, not a
// zero-relevance signal — returning 0 would poison anything fit against it.
func TestScoreErrorsOnEmptyProbs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(classifyResponse{Data: []struct {
			Probs []float64 `json:"probs"`
		}{{Probs: nil}}})
	}))
	defer srv.Close()

	cls, _ := New(Config{BaseURL: srv.URL}).(core.ScorerProvider).Scorer("")
	if _, err := cls.Score(context.Background(), "q", []string{"d"}); err == nil {
		t.Fatal("empty probs must error")
	}
}

// TestScoreEmptyCandidates returns nil without calling the endpoint.
func TestScoreEmptyCandidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the endpoint for zero candidates")
	}))
	defer srv.Close()

	cls, _ := New(Config{BaseURL: srv.URL}).(core.ScorerProvider).Scorer("")
	scores, err := cls.Score(context.Background(), "q", nil)
	if err != nil || scores != nil {
		t.Fatalf("empty candidates: got scores=%v err=%v, want nil,nil", scores, err)
	}
}

// TestProviderImplementsScorerOnly verifies the adapter's role surface: zerank
// provides only the ScorerProvider role.
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
