// Package zerank adapts the zeroentropy/zerank-1-small cross-encoder reranker
// served by vLLM. zerank-1-small is a Qwen3-1.7B causal LM whose reranker recipe
// (modeling_zeranker.py) reads the raw "Yes" logit at the assistant generation
// position of a system=query / user=document chat prompt and maps it with
// sigmoid(logit / 5).
//
// vLLM serves this natively as a SCORE model, not a generative one: the image's
// --hf-overrides re-head it to Qwen3ForSequenceClassification with
// classifier_from_token ["Yes"] and method "no_post_processing", so the "Yes" row
// of the lm_head becomes the single classifier weight — the /classify pooled
// logit IS that raw "Yes" logit. This adapter posts the chat-formatted prompt to
// /classify with use_activation=false (raw logit, not vLLM's built-in sigmoid,
// whose missing /5 temperature saturates every score to ~1.0) and applies
// sigmoid(logit / 5) itself.
//
// This replaced a generative recipe (chat/completions + next-token "Yes" logprob)
// that could not read a pre-softmax logit off the OpenAI API: it forced
// enable_thinking=false, which injected an empty <think></think> block the
// fine-tune never saw, and the model derailed into prose instead of answering.
package zerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"

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

// New creates a zerank provider. Implements ScorerProvider only — the model is a
// cross-encoder reranker served as a vLLM score model.
func New(cfg Config) any {
	if cfg.Model == "" {
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

// scoreTemperature is zerank's calibration constant: the model card maps the raw
// "Yes" logit to a probability with sigmoid(logit / 5.0).
const scoreTemperature = 5.0

type classifyRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"` // one pre-formatted chat prompt per candidate
	// UseActivation=false returns the raw classifier logit. vLLM's default
	// activation is a plain sigmoid(logit) with no temperature, which saturates
	// in-distribution logits (~10-17) to 1.0 and destroys ranking signal; the
	// /scoreTemperature below is what keeps scores separable.
	UseActivation bool `json:"use_activation"`
}

type classifyResponse struct {
	Data []struct {
		Probs []float64 `json:"probs"` // single-class head -> [raw "Yes" logit]
	} `json:"data"`
}

// chatPrompt formats a (query, document) pair as the reranker input: query as
// the system turn, document as the user turn, ending with the assistant
// generation prompt. This is byte-identical to the model's
// apply_chat_template(add_generation_prompt=True) (verified against vLLM's
// tokenizer), so the last-token pooled logit is read at the exact position
// modeling_zeranker.py scores. vLLM's own messages-mode drops the generation
// prompt (pooling the user-turn end instead), which is why the prompt is built
// here and sent as raw input.
func chatPrompt(query, document string) string {
	return "<|im_start|>system\n" + query + "<|im_end|>\n" +
		"<|im_start|>user\n" + document + "<|im_end|>\n" +
		"<|im_start|>assistant\n"
}

// Score returns the zerank relevance score in [0,1] for each (query, candidate)
// pair. All candidates go in one batched /classify call.
func (s *scorer) Score(ctx context.Context, query string, candidates []string) ([]float64, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	inputs := make([]string, len(candidates))
	for i, doc := range candidates {
		inputs[i] = chatPrompt(query, doc)
	}
	body, err := json.Marshal(classifyRequest{
		Model:         s.model,
		Input:         inputs,
		UseActivation: false,
	})
	if err != nil {
		return nil, err
	}

	var resp classifyResponse
	err = retry.Do(ctx, retry.LocalServicePolicy(), retry.LogRetryEvent("zerank score"), func(ctx context.Context) error {
		httpReq, err := http.NewRequest("POST", s.baseURL+"/classify", bytes.NewReader(body))
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
		return nil, err
	}

	if len(resp.Data) != len(candidates) {
		return nil, fmt.Errorf("%w: zerank/vllm scored %d of %d candidates",
			core.ErrProviderUnavailable, len(resp.Data), len(candidates))
	}
	out := make([]float64, len(candidates))
	for i := range resp.Data {
		if len(resp.Data[i].Probs) == 0 {
			return nil, fmt.Errorf("%w: zerank/vllm returned no logit for candidate %d",
				core.ErrProviderUnavailable, i)
		}
		out[i] = 1.0 / (1.0 + math.Exp(-resp.Data[i].Probs[0]/scoreTemperature))
	}
	return out, nil
}
