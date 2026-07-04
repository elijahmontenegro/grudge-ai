package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

func userMsg(text string) *llmv1.LLMMessage {
	return &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_USER,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: text}}},
		},
	}
}

// Streaming requests must ask for the usage frame — OpenAI omits
// usage from streams entirely unless stream_options.include_usage is
// set. Non-streaming requests must not carry the field.
func TestToChatRequest_StreamOptionsIncludeUsage(t *testing.T) {
	req := &llmv1.CompletionRequest{Messages: []*llmv1.LLMMessage{userMsg("hi")}}

	streamed, err := json.Marshal(toChatRequest("m", req, true))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(streamed), `"stream_options":{"include_usage":true}`) {
		t.Fatalf("streaming request missing stream_options: %s", streamed)
	}

	plain, err := json.Marshal(toChatRequest("m", req, false))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "stream_options") {
		t.Fatalf("non-streaming request carries stream_options: %s", plain)
	}
}

// End-to-end stream path: the request on the wire carries
// include_usage, and the final [DONE] chunk surfaces the usage frame
// the server sent (OpenAI delivers it as a choices-less frame).
func TestStream_EmitsAccumulatedUsage(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(
			"data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":123,\"completion_tokens\":9}}\n" +
				"data: [DONE]\n"))
	}))
	t.Cleanup(srv.Close)

	p := New(Config{BaseURL: srv.URL}).(*provider)
	ci, err := p.Completer("m")
	if err != nil {
		t.Fatalf("Completer: %v", err)
	}
	c := ci.(*completer)

	var final *llmv1.StreamChunk
	for chunk, err := range c.Stream(context.Background(), &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{userMsg("hi")},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if chunk.Done {
			final = chunk
		}
	}
	if !strings.Contains(string(gotBody), `"include_usage":true`) {
		t.Fatalf("wire request missing include_usage: %s", gotBody)
	}
	if final == nil || final.Usage == nil {
		t.Fatalf("no terminal chunk with usage: %+v", final)
	}
	if final.Usage.PromptTokens != 123 || final.Usage.CompletionTokens != 9 {
		t.Fatalf("usage = %d/%d, want 123/9", final.Usage.PromptTokens, final.Usage.CompletionTokens)
	}
}

// CountText mirrors toChatMessage/toMultipart: text blocks only —
// thinking, tool calls, and tool results are never sent by this
// codec, so they must not count against the budget.
func TestCountText_TextBlocksOnly(t *testing.T) {
	m := &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "answer"}}},
			{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "SECRET"}}},
			{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{Id: "c1", Name: "Bash", Arguments: "ARGS"}}},
			{Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{ToolCallId: "c1", Content: "RESULT"}}},
		},
	}
	got := CountText(m)
	if !strings.Contains(got, "answer") {
		t.Fatalf("CountText missing text block: %q", got)
	}
	for _, banned := range []string{"SECRET", "ARGS", "RESULT"} {
		if strings.Contains(got, banned) {
			t.Fatalf("CountText includes %q, which openai never sends: %q", banned, got)
		}
	}
}
