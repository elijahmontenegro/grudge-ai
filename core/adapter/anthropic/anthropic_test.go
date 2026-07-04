package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

func testCompleter(t *testing.T, handler http.HandlerFunc) *completer {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p := New(Config{APIKey: "test", BaseURL: srv.URL}).(*provider)
	c, err := p.Completer("claude-test")
	if err != nil {
		t.Fatalf("Completer: %v", err)
	}
	return c.(*completer)
}

func userMsg(text string) *llmv1.LLMMessage {
	return &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_USER,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: text}}},
		},
	}
}

// Anthropic reports cache tokens SEPARATELY from input_tokens — the
// true prompt total is the three-way sum. An adapter mapping
// input_tokens alone undercounts by the whole cached prefix.
func TestComplete_PromptTokensSumCacheFields(t *testing.T) {
	c := testCompleter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"id": "msg_1",
			"model": "claude-test",
			"content": [{"type":"text","text":"hello"}],
			"usage": {
				"input_tokens": 100,
				"output_tokens": 50,
				"cache_creation_input_tokens": 200,
				"cache_read_input_tokens": 700
			}
		}`))
	})

	resp, err := c.Complete(context.Background(), &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{userMsg("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Usage.PromptTokens != 1000 {
		t.Fatalf("PromptTokens = %d, want 1000 (100 input + 200 cache-write + 700 cache-read)", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 50 {
		t.Fatalf("CompletionTokens = %d, want 50", resp.Usage.CompletionTokens)
	}
}

// The prompt-side counts arrive ONLY in message_start (nested at
// message.usage); message_delta carries the output side. Before the
// message_start case existed, every streamed completion reported
// PromptTokens = 0.
func TestStream_UsageFromMessageStartAndDelta(t *testing.T) {
	c := testCompleter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(
			"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":80,\"cache_creation_input_tokens\":20,\"cache_read_input_tokens\":900}}}\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n" +
				"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":42}}\n"))
	})

	var final *llmv1.StreamChunk
	var text strings.Builder
	for chunk, err := range c.Stream(context.Background(), &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{userMsg("hi")},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if tb := chunk.GetText(); tb != nil {
			text.WriteString(tb.Text)
		}
		if chunk.Done {
			final = chunk
		}
	}
	if text.String() != "hi" {
		t.Fatalf("streamed text = %q, want %q", text.String(), "hi")
	}
	if final == nil || final.Usage == nil {
		t.Fatalf("no terminal chunk with usage: %+v", final)
	}
	if final.Usage.PromptTokens != 1000 {
		t.Fatalf("PromptTokens = %d, want 1000 (message_start three-way sum)", final.Usage.PromptTokens)
	}
	if final.Usage.CompletionTokens != 42 {
		t.Fatalf("CompletionTokens = %d, want 42", final.Usage.CompletionTokens)
	}
}

// A stream that dies after message_start but before message_delta
// must not fabricate a terminal usage chunk — no usage means no
// observation, by contract.
func TestStream_DeathBeforeMessageDeltaYieldsNoUsage(t *testing.T) {
	c := testCompleter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(
			"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":80}}}\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n"))
	})

	for chunk, err := range c.Stream(context.Background(), &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{userMsg("hi")},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if chunk.Usage != nil {
			t.Fatalf("chunk carries usage despite truncated stream: %+v", chunk)
		}
	}
}

// CountText mirrors toAPIContent: text + tool_use + tool_result are
// sent; thinking and attachments are not.
func TestCountText_MirrorsToAPIContent(t *testing.T) {
	m := &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "answer"}}},
			{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "SECRET-REASONING"}}},
			{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{Id: "c1", Name: "Bash", Arguments: `{"cmd":"ls"}`}}},
			{Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{ToolCallId: "c1", Content: "a.txt"}}},
		},
	}
	got := CountText(m)
	for _, want := range []string{"answer", "Bash", `{"cmd":"ls"}`, "a.txt"} {
		if !strings.Contains(got, want) {
			t.Fatalf("CountText missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "SECRET-REASONING") {
		t.Fatalf("CountText includes thinking, which anthropic never sends: %q", got)
	}
}
