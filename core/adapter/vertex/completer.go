package vertex

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

type completer struct {
	client *genai.Client
	model  string
}

func (c *completer) Complete(ctx context.Context, req *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error) {
	contents, cfg := c.encode(req)
	resp, err := c.client.Models.GenerateContent(ctx, c.model, contents, cfg)
	if err != nil {
		return nil, fmt.Errorf("vertex generate: %w", err)
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("vertex: no candidates in response")
	}
	msg := genaicodec.ContentToProto(resp.Candidates[0].Content)
	msg.Role = threadv1.Role_ROLE_ASSISTANT
	out := &llmv1.CompletionResponse{
		Model:        c.model,
		Message:      msg,
		FinishReason: normalizeFinish(resp.Candidates[0].FinishReason),
	}
	if u := resp.UsageMetadata; u != nil {
		out.Usage = &llmv1.Usage{PromptTokens: u.PromptTokenCount, CompletionTokens: u.CandidatesTokenCount}
	}
	return out, nil
}

func (c *completer) Stream(ctx context.Context, req *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error] {
	return func(yield func(*llmv1.StreamChunk, error) bool) {
		contents, cfg := c.encode(req)
		var lastFinish string
		var usage *llmv1.Usage
		for resp, err := range c.client.Models.GenerateContentStream(ctx, c.model, contents, cfg) {
			if err != nil {
				yield(nil, fmt.Errorf("vertex stream: %w", err))
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
// nil for empty/unmapped parts.
func partToChunk(p *genai.Part) *llmv1.StreamChunk {
	switch {
	case p.Text != "" && p.Thought:
		return &llmv1.StreamChunk{Delta: &llmv1.StreamChunk_Thinking{Thinking: &threadv1.ThinkingContent{Text: p.Text}}}
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
	return "vertex-call-" + name
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
