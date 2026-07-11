package ollama

import (
	"strings"
	"testing"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// providerOpts must forward the caller's declared window as num_ctx —
// without it ollama serves the model default and silently truncates
// the prompt head.
func TestProviderOpts_NumCtx(t *testing.T) {
	cw := int32(8192)
	opts := providerOpts(&llmv1.CompletionRequest{ContextWindowTokens: &cw})
	if got, ok := opts["num_ctx"].(int32); !ok || got != 8192 {
		t.Fatalf("num_ctx = %v, want 8192", opts["num_ctx"])
	}

	if opts := providerOpts(&llmv1.CompletionRequest{}); opts != nil {
		if _, ok := opts["num_ctx"]; ok {
			t.Fatalf("num_ctx present without a declared window: %v", opts)
		}
	}

	zero := int32(0)
	if opts := providerOpts(&llmv1.CompletionRequest{ContextWindowTokens: &zero}); opts != nil {
		if _, ok := opts["num_ctx"]; ok {
			t.Fatalf("num_ctx present for zero window: %v", opts)
		}
	}
}

// CountText mirrors toLlamaMsgs' atom extraction: text, thinking,
// tool calls, and tool results all reach the wire; attachments never
// do.
func TestCountText_IncludesThinkingAndTools(t *testing.T) {
	m := msg(threadv1.Role_ROLE_ASSISTANT,
		textBlock("answer"),
		thinkingBlock("REASONING"),
		toolCallBlock("c1", "Bash", `{"cmd":"ls"}`),
		toolResultBlock("c1", "a.txt"),
		&threadv1.ContentBlock{Block: &threadv1.ContentBlock_Attachment{Attachment: &threadv1.AttachmentContent{
			Filename: "SECRET.pdf", Path: "/x", InlinedText: "INLINED",
		}}},
	)
	got := CountText(m)
	for _, want := range []string{"answer", "REASONING", "Bash", `{"cmd":"ls"}`, "a.txt"} {
		if !strings.Contains(got, want) {
			t.Fatalf("CountText missing %q: %q", want, got)
		}
	}
	for _, banned := range []string{"SECRET.pdf", "INLINED"} {
		if strings.Contains(got, banned) {
			t.Fatalf("CountText includes attachment content %q, which the codec never sends: %q", banned, got)
		}
	}
}
