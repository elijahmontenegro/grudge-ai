package proxy_test

import (
	"strings"
	"testing"

	"github.com/emontenegr/spidey/core"
	_ "github.com/emontenegr/spidey/core/adapter/anthropic"
	_ "github.com/emontenegr/spidey/core/adapter/openai"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// TestCodec_DecodeResponseParity — the proxy's contract is that the
// same model output, served by either an OpenAI-shaped or
// Anthropic-shaped upstream, decodes to a canonical
// pb.CompletionResponse with equivalent content. RRC + downstream
// don't care which adapter the bytes came from; they read the
// canonical proto.
//
// This test constructs both wire shapes by hand (rather than
// round-tripping through Encode), so it pins the canonical contract
// independent of any single adapter's encoder choices.
func TestCodec_DecodeResponseParity(t *testing.T) {
	openaiBody := []byte(`{
		"id": "resp-1",
		"object": "chat.completion",
		"model": "test-model",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": "the answer is 42"},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`)

	anthropicBody := []byte(`{
		"id": "resp-1",
		"type": "message",
		"role": "assistant",
		"model": "test-model",
		"content": [{"type": "text", "text": "the answer is 42"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)

	openai, ok := core.LookupCodec("openai")
	if !ok {
		t.Fatal("openai codec not registered")
	}
	anthropic, ok := core.LookupCodec("anthropic")
	if !ok {
		t.Fatal("anthropic codec not registered")
	}

	openaiResp, err := openai.DecodeResponse(openaiBody)
	if err != nil {
		t.Fatalf("openai DecodeResponse: %v", err)
	}
	anthropicResp, err := anthropic.DecodeResponse(anthropicBody)
	if err != nil {
		t.Fatalf("anthropic DecodeResponse: %v", err)
	}

	if got := textOf(openaiResp.Message); got != "the answer is 42" {
		t.Errorf("openai canonical text: got %q want %q", got, "the answer is 42")
	}
	if got := textOf(anthropicResp.Message); got != "the answer is 42" {
		t.Errorf("anthropic canonical text: got %q want %q", got, "the answer is 42")
	}
	if openaiResp.Model != anthropicResp.Model {
		t.Errorf("model field disagree: openai=%q anthropic=%q", openaiResp.Model, anthropicResp.Model)
	}
	if openaiResp.Usage.PromptTokens != anthropicResp.Usage.PromptTokens {
		t.Errorf("prompt tokens disagree: openai=%d anthropic=%d",
			openaiResp.Usage.PromptTokens, anthropicResp.Usage.PromptTokens)
	}
	if openaiResp.Usage.CompletionTokens != anthropicResp.Usage.CompletionTokens {
		t.Errorf("completion tokens disagree: openai=%d anthropic=%d",
			openaiResp.Usage.CompletionTokens, anthropicResp.Usage.CompletionTokens)
	}
	if openaiResp.Message.Role != anthropicResp.Message.Role {
		t.Errorf("role disagree: openai=%v anthropic=%v",
			openaiResp.Message.Role, anthropicResp.Message.Role)
	}
}

// TestCodec_DecodeRequestParity — request side: a user prompt
// expressed in either OpenAI or Anthropic wire format decodes to a
// canonical request with the same user content. (Anthropic hoists
// system to a top-level field; OpenAI puts it as the first message
// — both must canonicalize to a ROLE_SYSTEM LLMMessage at position
// 0, with the user turn at position 1.)
func TestCodec_DecodeRequestParity(t *testing.T) {
	openaiBody := []byte(`{
		"model": "test-model",
		"messages": [
			{"role": "system", "content": "you are helpful"},
			{"role": "user", "content": "hello"}
		]
	}`)
	anthropicBody := []byte(`{
		"model": "test-model",
		"system": "you are helpful",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello"}]}
		],
		"max_tokens": 1024
	}`)

	openai, _ := core.LookupCodec("openai")
	anthropic, _ := core.LookupCodec("anthropic")

	openaiReq, err := openai.DecodeRequest(openaiBody)
	if err != nil {
		t.Fatalf("openai DecodeRequest: %v", err)
	}
	anthropicReq, err := anthropic.DecodeRequest(anthropicBody)
	if err != nil {
		t.Fatalf("anthropic DecodeRequest: %v", err)
	}

	if len(openaiReq.Messages) != 2 {
		t.Fatalf("openai messages: got %d want 2", len(openaiReq.Messages))
	}
	if len(anthropicReq.Messages) != 2 {
		t.Fatalf("anthropic messages: got %d want 2", len(anthropicReq.Messages))
	}

	for i := range openaiReq.Messages {
		o := openaiReq.Messages[i]
		a := anthropicReq.Messages[i]
		if o.Role != a.Role {
			t.Errorf("Messages[%d] role: openai=%v anthropic=%v", i, o.Role, a.Role)
		}
		ot := textOf(o)
		at := textOf(a)
		if ot != at {
			t.Errorf("Messages[%d] text: openai=%q anthropic=%q", i, ot, at)
		}
	}

	if openaiReq.Messages[0].Role != pb.Role_ROLE_SYSTEM {
		t.Errorf("openai system msg role: got %v want ROLE_SYSTEM", openaiReq.Messages[0].Role)
	}
	if anthropicReq.Messages[0].Role != pb.Role_ROLE_SYSTEM {
		t.Errorf("anthropic system msg role: got %v want ROLE_SYSTEM", anthropicReq.Messages[0].Role)
	}
}

func textOf(m *pb.LLMMessage) string {
	if m == nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range m.Content {
		if t := b.GetText(); t != nil {
			sb.WriteString(t.GetText())
		}
	}
	return sb.String()
}
