// Package zerank adapts the zeroentropy/zerank-1-small cross-encoder
// reranker served by vLLM via OpenAI-compatible completion API. The
// scoring recipe is the model card's: chat-template prompt with
// system=query and user=document, take the next-token "Yes" logprob,
// apply sigmoid(logit / 5.0).
//
// Why vLLM rather than TEI: zerank-1-small is a Qwen3-1.7B causal LM
// repurposed as a reranker. TEI's /rerank endpoint only serves
// BERT-class encoder classifiers (CamemBERT, XLM-RoBERTa, GTE,
// ModernBert), not causal LMs — it has no way to extract the
// Yes-token logprob this recipe requires. vLLM is the right serving
// stack: causal-LM scoring with logprobs+top_logprobs, the
// chat_template_kwargs.enable_thinking=false extension that
// disables Qwen3's <think> prelude, plus prefix KV caching that
// makes scoring N candidates against 1 query reuse the
// chat-template prefix's KV state across the N forward passes.
package zerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/core/httpc"
	"github.com/elijahmontenegro/grudge/core/httpc/retry"
)

// Config for the zerank (vLLM-served zerank-1-small) provider.
type Config struct {
	BaseURL string // e.g., "http://localhost:8000"
	Model   string // e.g., "zeroentropy/zerank-1-small"
}

type provider struct {
	cfg    Config
	client *httpc.Client
}

// New creates a zerank provider. Implements ScorerProvider only
// — the model is a cross-encoder reranker. vLLM can technically
// serve completions for the same weights, but this adapter is
// purpose-built for reranking; callers wanting completion against
// these weights configure the vllm adapter instead.
func New(cfg Config) any {
	if cfg.Model == "" {
		// 1.7B variant — fits the 12GB co-resident budget alongside
		// Qwen3-Embedding-0.6B on TEI. The 4B zerank-2 trains the
		// same recipe but doesn't co-reside on consumer GPUs.
		cfg.Model = "zeroentropy/zerank-1-small"
	}
	return &provider{
		cfg:    cfg,
		client: httpc.New(httpc.TimeoutTEI, nil),
	}
}

func (p *provider) Scorer(_ string) (core.Scorer, error) {
	return &scorer{
		baseURL: p.cfg.BaseURL,
		model:   p.cfg.Model,
		client:  p.client,
	}, nil
}

// --- Scorer ---

type scorer struct {
	baseURL string
	model   string
	client  *httpc.Client
}

// Per the zerank-1-small model card (recipe identical to zerank-2,
// which the card cross-references): system message holds the query,
// user message holds the document, sample 1 token with logprobs to
// extract "Yes". The model card's score formula is sigmoid(yes_logit / 5);
// we replicate it by extracting top_logprobs at the next-token
// position and finding the entry whose token is "Yes" (or "yes" —
// case-insensitive match).
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatTemplateKwargs struct {
	EnableThinking bool `json:"enable_thinking"`
}

type completionRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
	Logprobs    bool          `json:"logprobs"`
	TopLogprobs int           `json:"top_logprobs"`
	// ChatTemplateKwargs disables Qwen3-style <think> reasoning emission
	// at the start of the assistant turn. Without it, zerank-1-small
	// (and zerank-2 — the recipe is shared) emits "<think>" as the
	// most-likely first token under vLLM's chat template, pushing "Yes"
	// out of the top-20 logprobs we sample. With enable_thinking=false
	// the assistant turn starts directly at the answer position; "Yes"
	// shows up near the top of the next-token distribution, making the
	// sigmoid(yes_logit/5) score recoverable.
	ChatTemplateKwargs chatTemplateKwargs `json:"chat_template_kwargs"`
}

type tokenLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
}

type tokenChoiceLogprobs struct {
	Content []struct {
		Token       string         `json:"token"`
		Logprob     float64        `json:"logprob"`
		TopLogprobs []tokenLogprob `json:"top_logprobs"`
	} `json:"content"`
}

type completionResponse struct {
	Choices []struct {
		Logprobs tokenChoiceLogprobs `json:"logprobs"`
	} `json:"choices"`
}

// Score returns the zerank relevance score for each (query, candidate) pair.
// One vLLM call per candidate — prefix caching makes the per-call cost
// dominated by candidate-side tokens, not the duplicated query prefix.
func (s *scorer) Score(ctx context.Context, query string, candidates []string) ([]float64, error) {
	out := make([]float64, len(candidates))
	for i, doc := range candidates {
		score, err := s.scoreOne(ctx, query, doc)
		if err != nil {
			return nil, err
		}
		out[i] = score
	}
	return out, nil
}

func (s *scorer) scoreOne(ctx context.Context, query, document string) (float64, error) {
	req := completionRequest{
		Model: s.model,
		Messages: []chatMessage{
			{Role: "system", Content: query},
			{Role: "user", Content: document},
		},
		// 6 tokens, not 1: enable_thinking=false is supposed to bypass
		// Qwen3's reasoning prelude, but vLLM template versions have
		// been observed emitting stray thinking scaffold ("</think>",
		// newlines) ahead of the answer token. Generating a small
		// window lets scoring skip past benign scaffold to the actual
		// Yes/No position instead of breaking on it. Bounded and tiny
		// relative to prefill cost.
		MaxTokens:   6,
		Temperature: 0,
		Logprobs:    true,
		// 20 covers vocab dispersion at the answer position
		// without paying for the full vocab. "Yes" / "yes" / variants
		// reliably land in the top-20 for an in-distribution input.
		TopLogprobs:        20,
		ChatTemplateKwargs: chatTemplateKwargs{EnableThinking: false},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}

	var resp completionResponse
	err = retry.Do(ctx, retry.LocalServicePolicy(), retry.LogRetryEvent("zerank rerank"), func(ctx context.Context) error {
		httpReq, err := http.NewRequest("POST", s.baseURL+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")

		respBody, status, err := s.client.DoJSON(ctx, httpReq)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("%w: zerank/vllm returned %d: %s",
				core.ErrProviderUnavailable, status, respBody)
		}
		return json.Unmarshal(respBody, &resp)
	})
	if err != nil {
		return 0, err
	}
	if len(resp.Choices) == 0 || len(resp.Choices[0].Logprobs.Content) == 0 {
		return 0, fmt.Errorf("%w: zerank/vllm returned empty logprobs",
			core.ErrProviderUnavailable)
	}

	// Skip benign thinking scaffold ("</think>", "<think>", bare
	// whitespace tokens) to the first real answer position. A healthy
	// template answers at position 0 and the scan is a no-op; a
	// template that leaks its thinking prelude still yields a correct
	// score instead of a break. Only KNOWN scaffold is skipped — the
	// first non-scaffold position must be the Yes/No answer or scoring
	// fails loudly below.
	content := resp.Choices[0].Logprobs.Content
	answerIdx := -1
	for i := range content {
		if !tokenIsScaffold(content[i].Token) {
			answerIdx = i
			break
		}
	}
	if answerIdx == -1 {
		return 0, fmt.Errorf("%w: only thinking-scaffold tokens in %d generated positions — chat-template drift at endpoint?",
			core.ErrProviderUnavailable, len(content))
	}

	first := content[answerIdx]
	// The generated token at the answer position must BE the answer. At
	// temperature 0 a healthy zerank's argmax is Yes/No; anything else
	// (a reasoning trace opening with prose, an open think fence) means
	// the position is not the answer head, and reading Yes/No out of a
	// prose distribution's top-20 would score garbage silently.
	if !tokenIsYes(first.Token) && !tokenIsNo(first.Token) {
		return 0, fmt.Errorf("%w: answer position generated %q, not Yes/No — chat-template drift or wrong model at endpoint?",
			core.ErrProviderUnavailable, first.Token)
	}
	yesLogprob := math.Inf(-1)
	noLogprob := math.Inf(-1)
	for _, tl := range first.TopLogprobs {
		// Match "Yes" / "yes" and "No" / "no", including leading-space
		// variants emitted by chat-template tokenizers (e.g. " Yes",
		// " No" with the leading space-tokenization). zerank's
		// fine-tuning concentrates next-token mass on these two
		// surface forms.
		switch {
		case tokenIsYes(tl.Token):
			if tl.Logprob > yesLogprob {
				yesLogprob = tl.Logprob
			}
		case tokenIsNo(tl.Token):
			if tl.Logprob > noLogprob {
				noLogprob = tl.Logprob
			}
		}
	}
	if math.IsInf(yesLogprob, -1) && math.IsInf(noLogprob, -1) {
		// NEITHER Yes nor No in the top-K: the model is not answering
		// the binary relevance question at all. That is a recipe
		// violation — chat-template drift (a thinking prelude
		// reappearing ahead of the answer token) or a non-zerank model
		// at the endpoint — not a relevance signal. Returning 0 here
		// would let every pair silently score 0.0 under an HTTP 200,
		// and anything fit against those scores downstream would be
		// fit against garbage. Fail loudly instead.
		return 0, fmt.Errorf("%w: neither Yes nor No in top-%d logprobs — chat-template drift or wrong model at endpoint?",
			core.ErrProviderUnavailable, len(first.TopLogprobs))
	}
	if math.IsInf(yesLogprob, -1) {
		// "Yes" not in the top-K logprobs but "No" is — the model
		// answered the binary question and is confident "no". Score 0.
		return 0, nil
	}
	// Recover the binary log-odds (Yes vs No) from the post-softmax
	// logprobs the OpenAI-compat API exposes. zerank's model card
	// recipe is `sigmoid(yes_logit / 5)` where yes_logit is the
	// PRE-softmax Yes score — not directly accessible from the API.
	// The faithful reconstruction is `ln(P_yes / P_no) =
	// logprob_yes - logprob_no`, which is the binary logit the
	// cross-encoder head implicitly computes when Yes and No
	// dominate the next-token distribution (the case zerank was
	// trained for).
	//
	// If No is outside the top-K (very confident Yes), we use the
	// floor of the observed top-K logprobs as an upper bound on
	// no_logprob. P(No) < P(20th-ranked token) is a strict
	// implication of "No not in top-K", so this is principled,
	// not a guess.
	if math.IsInf(noLogprob, -1) {
		// Floor: the smallest logprob in this top-K window. Any
		// missing token (including No) has logprob strictly less
		// than this floor.
		floor := math.Inf(-1)
		for _, tl := range first.TopLogprobs {
			if !math.IsInf(tl.Logprob, -1) && (math.IsInf(floor, -1) || tl.Logprob < floor) {
				floor = tl.Logprob
			}
		}
		if math.IsInf(floor, -1) {
			// Degenerate: only -inf entries. Treat as no-info → 0.5
			// would mislead; the only Yes signal we have is its own
			// logprob, so use a bare yes_logprob/5 sigmoid — capped
			// at 0.5 but monotonic in Yes confidence.
			return 1.0 / (1.0 + math.Exp(-yesLogprob/5.0)), nil
		}
		noLogprob = floor
	}
	binaryLogit := yesLogprob - noLogprob
	return 1.0 / (1.0 + math.Exp(-binaryLogit/5.0)), nil
}

// tokenIsScaffold reports whether token is benign thinking-prelude
// scaffold: the CLOSE fence (the template pre-opened an empty think
// block and the model immediately closes it — the observed harmless
// case) or pure whitespace. The OPEN fence is deliberately NOT
// scaffold: a generated "<think>" means the model is ENTERING a
// reasoning trace — proof enable_thinking was ignored — and scoring
// must fail loudly rather than scan into prose. Full TrimSpace (not
// just leading spaces/tabs): the prelude's separators are newline
// tokens.
func tokenIsScaffold(token string) bool {
	t := strings.TrimSpace(token)
	if t == "" {
		return true // pure-whitespace token
	}
	return t == "</think>"
}

func tokenIsYes(token string) bool {
	t := stripLeadingWS(token)
	return t == "Yes" || t == "yes"
}

func tokenIsNo(token string) bool {
	t := stripLeadingWS(token)
	return t == "No" || t == "no"
}

func stripLeadingWS(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	return s
}
