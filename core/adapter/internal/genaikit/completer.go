// Package genaikit is the shared genai-SDK completer/embedder/counting
// implementation behind both the vertex adapter (BackendVertexAI, ADC)
// and the googleai adapter (BackendGeminiAPI, API-key). Only client
// construction and auth differ between the two — the request codec,
// response decode, streaming, and counting projection are identical,
// so they live once here rather than duplicated per adapter.
package genaikit

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"

	"github.com/elijahmontenegro/grudge/core/genaicodec"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"google.golang.org/genai"
)

// Completer implements core.Completer over a genai client. Name
// prefixes error messages ("vertex" | "googleai") so a failure is
// traceable to its adapter identity despite the shared implementation.
type Completer struct {
	Client *genai.Client
	Model  string
	Name   string
}

func (c *Completer) Complete(ctx context.Context, req *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error) {
	contents, cfg, err := c.encode(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.Client.Models.GenerateContent(ctx, c.Model, contents, cfg)
	if err != nil {
		return nil, fmt.Errorf("%s generate: %w", c.Name, err)
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("%s: no candidates in response", c.Name)
	}
	msg := genaicodec.ContentToProto(resp.Candidates[0].Content)
	msg.Role = threadv1.Role_ROLE_ASSISTANT
	out := &llmv1.CompletionResponse{
		Model:        c.Model,
		Message:      msg,
		FinishReason: normalizeFinish(resp.Candidates[0].FinishReason),
	}
	if u := resp.UsageMetadata; u != nil {
		out.Usage = &llmv1.Usage{PromptTokens: u.PromptTokenCount, CompletionTokens: u.CandidatesTokenCount}
	}
	return out, nil
}

func (c *Completer) Stream(ctx context.Context, req *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error] {
	return func(yield func(*llmv1.StreamChunk, error) bool) {
		contents, cfg, err := c.encode(req)
		if err != nil {
			yield(nil, err)
			return
		}
		var lastFinish string
		var usage *llmv1.Usage
		for resp, err := range c.Client.Models.GenerateContentStream(ctx, c.Model, contents, cfg) {
			if err != nil {
				yield(nil, fmt.Errorf("%s stream: %w", c.Name, err))
				return
			}
			if len(resp.Candidates) == 0 {
				continue
			}
			cand := resp.Candidates[0]
			if cand.FinishReason != "" {
				lastFinish = normalizeFinish(cand.FinishReason)
			}
			if u := resp.UsageMetadata; u != nil {
				usage = &llmv1.Usage{PromptTokens: u.PromptTokenCount, CompletionTokens: u.CandidatesTokenCount}
			}
			if cand.Content == nil {
				continue
			}
			for _, part := range cand.Content.Parts {
				chunk := partToChunk(part)
				if chunk == nil {
					continue
				}
				if !yield(chunk, nil) {
					return
				}
			}
		}
		// Terminal chunk carries the finish reason + usage.
		yield(&llmv1.StreamChunk{Done: true, FinishReason: lastFinish, Usage: usage}, nil)
	}
}

// partToChunk maps one genai response Part to a StreamChunk delta. Returns
// nil for empty/unmapped parts. ThoughtSignature is Part-level, so a
// signed thinking or function-call part carries it directly on the
// chunk that part produces — no separate terminator chunk is needed
// (contrast Anthropic's SSE protocol, which delivers the signature as
// a later, independent delta and needs one).
func partToChunk(p *genai.Part) *llmv1.StreamChunk {
	switch {
	case (p.Text != "" || len(p.ThoughtSignature) > 0) && p.Thought:
		return &llmv1.StreamChunk{Delta: &llmv1.StreamChunk_Thinking{Thinking: &threadv1.ThinkingContent{
			Text:      p.Text,
			Signature: p.ThoughtSignature,
		}}}
	case p.Text != "":
		return &llmv1.StreamChunk{Delta: &llmv1.StreamChunk_Text{Text: &threadv1.TextContent{Text: p.Text}}}
	case p.FunctionCall != nil:
		fc := p.FunctionCall
		args := "{}"
		if fc.Args != nil {
			if b, err := json.Marshal(fc.Args); err == nil {
				args = string(b)
			}
		}
		return &llmv1.StreamChunk{Delta: &llmv1.StreamChunk_ToolCall{ToolCall: &threadv1.ToolCallContent{
			Id:        synthCallID(fc.ID, fc.Name),
			Name:      fc.Name,
			Arguments: args,
			Signature: p.ThoughtSignature,
		}}}
	default:
		return nil
	}
}

// synthCallID gives a Gemini function call a stable id when the SDK omits one
// (Gemini frequently returns an empty FunctionCall.ID). grudge correlates
// tool results to calls by id, so a missing id would break multi-turn tool
// loops. Deterministic from the function name so the matching FunctionResponse
// (which grudge threads back by the same id) lines up.
func synthCallID(id, name string) string {
	if id != "" {
		return id
	}
	return "genai-call-" + name
}

func normalizeFinish(fr genai.FinishReason) string {
	switch fr {
	case genai.FinishReasonStop:
		return "stop"
	case genai.FinishReasonMaxTokens:
		return "length"
	case "":
		return ""
	default:
		return string(fr)
	}
}
