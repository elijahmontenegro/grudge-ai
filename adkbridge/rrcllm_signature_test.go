package adkbridge

import (
	"testing"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// A signed thinking chunk and a signed tool-call chunk both reach the
// yielded ADK response with ThoughtSignature intact on the Part the
// bridge builds — the carriage the runner depends on to store
// signatures. The signature-only terminator (Anthropic's zero-text
// thinking chunk) must not fire OnStream, since it carries no new
// text to show.
func TestGenerateContent_CarriesThoughtSignatureToParts(t *testing.T) {
	llm := usageHarness(t, &chunkStreamer{chunks: []*llmv1.StreamChunk{
		{Delta: &llmv1.StreamChunk_Thinking{Thinking: &threadv1.ThinkingContent{Text: "let me check"}}},
		{Delta: &llmv1.StreamChunk_Thinking{Thinking: &threadv1.ThinkingContent{Signature: []byte("think-sig")}}},
		{Delta: &llmv1.StreamChunk_ToolCall{ToolCall: &threadv1.ToolCallContent{Id: "c1", Name: "read", Arguments: "{}", Signature: []byte("call-sig")}}},
		{Done: true, Usage: &llmv1.Usage{PromptTokens: 480, CompletionTokens: 7}},
	}})

	var streamCalls int
	llm.OnStream = func(delta, thinking string, done bool) {
		if !done {
			streamCalls++
		}
	}

	var thoughtSig, callSig []byte
	var sawSignatureOnlyPart bool
	for resp, err := range llm.GenerateContent(t.Context(), adkReq(), true) {
		if err != nil {
			t.Fatalf("GenerateContent: %v", err)
		}
		if resp.Content == nil {
			continue
		}
		for _, part := range resp.Content.Parts {
			switch {
			case part.Thought && part.Text == "" && len(part.ThoughtSignature) > 0:
				sawSignatureOnlyPart = true
				thoughtSig = part.ThoughtSignature
			case part.FunctionCall != nil:
				callSig = part.ThoughtSignature
			}
		}
	}

	if !sawSignatureOnlyPart {
		t.Fatal("no zero-text signature-bearing thought Part observed")
	}
	if string(thoughtSig) != "think-sig" {
		t.Fatalf("thought signature not carried to the Part: %q", thoughtSig)
	}
	if string(callSig) != "call-sig" {
		t.Fatalf("tool-call signature not carried to the Part: %q", callSig)
	}
	// Two OnStream(done=false) calls expected: the text-bearing
	// thinking delta ("let me check") and... nothing else — the
	// signature-only chunk must NOT have fired a second one.
	if streamCalls != 1 {
		t.Fatalf("OnStream(done=false) fired %d times, want 1 (signature-only chunk must not fire it)", streamCalls)
	}
}

// A thinking chunk that never receives a signature_delta streams
// through as an ordinary text-bearing Part — no signature field set,
// no zero-text terminator, matching today's unsigned-thinking
// behavior unchanged.
func TestGenerateContent_UnsignedThinkingHasNoSignaturePart(t *testing.T) {
	llm := usageHarness(t, &chunkStreamer{chunks: []*llmv1.StreamChunk{
		{Delta: &llmv1.StreamChunk_Thinking{Thinking: &threadv1.ThinkingContent{Text: "unsigned"}}},
		{Done: true, Usage: &llmv1.Usage{PromptTokens: 480, CompletionTokens: 7}},
	}})

	var sawSignature bool
	var sawText bool
	for resp, err := range llm.GenerateContent(t.Context(), adkReq(), true) {
		if err != nil {
			t.Fatalf("GenerateContent: %v", err)
		}
		if resp.Content == nil {
			continue
		}
		for _, part := range resp.Content.Parts {
			if part.Thought {
				if part.Text == "unsigned" {
					sawText = true
				}
				if len(part.ThoughtSignature) > 0 {
					sawSignature = true
				}
			}
		}
	}
	if !sawText {
		t.Fatal("unsigned thinking text not observed")
	}
	if sawSignature {
		t.Fatal("unsigned thinking must not carry a signature")
	}
}
