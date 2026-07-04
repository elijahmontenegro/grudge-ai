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

func toolCallMsg(id, name, args string) *llmv1.LLMMessage {
	return &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{Id: id, Name: name, Arguments: args}}},
		},
	}
}

func toolResultMsg(id, content string) *llmv1.LLMMessage {
	return &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{ToolCallId: id, Content: content}}},
		},
	}
}

func mustMarshalRequest(t *testing.T, req *llmv1.CompletionRequest, stream bool) string {
	t.Helper()
	creq, err := toChatRequest("m", req, stream)
	if err != nil {
		t.Fatalf("toChatRequest: %v", err)
	}
	b, err := json.Marshal(creq)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Streaming requests must ask for the usage frame — OpenAI omits
// usage from streams entirely unless stream_options.include_usage is
// set. Non-streaming requests must not carry the field.
func TestToChatRequest_StreamOptionsIncludeUsage(t *testing.T) {
	req := &llmv1.CompletionRequest{Messages: []*llmv1.LLMMessage{userMsg("hi")}}

	streamed := mustMarshalRequest(t, req, true)
	if !strings.Contains(streamed, `"stream_options":{"include_usage":true}`) {
		t.Fatalf("streaming request missing stream_options: %s", streamed)
	}

	plain := mustMarshalRequest(t, req, false)
	if strings.Contains(plain, "stream_options") {
		t.Fatalf("non-streaming request carries stream_options: %s", plain)
	}
}

// All four ToolChoice modes encode to OpenAI's documented shapes;
// unset/UNSPECIFIED omits the field entirely rather than defaulting
// to a literal "auto" on the wire.
func TestToChatRequest_ToolChoiceModes(t *testing.T) {
	base := &llmv1.CompletionRequest{Messages: []*llmv1.LLMMessage{userMsg("hi")}}

	if got := mustMarshalRequest(t, base, false); strings.Contains(got, "tool_choice") {
		t.Fatalf("unset ToolChoice must omit the field, got: %s", got)
	}

	cases := []struct {
		mode llmv1.ToolChoiceMode
		name string
		want string
	}{
		{llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_AUTO, "", `"tool_choice":"auto"`},
		{llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NONE, "", `"tool_choice":"none"`},
		{llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_REQUIRED, "", `"tool_choice":"required"`},
		{llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NAMED, "Bash", `"tool_choice":{"type":"function","function":{"name":"Bash"}}`},
	}
	for _, tc := range cases {
		req := &llmv1.CompletionRequest{
			Messages:   base.Messages,
			ToolChoice: &llmv1.ToolChoice{Mode: tc.mode, NamedTool: tc.name},
		}
		got := mustMarshalRequest(t, req, false)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("mode %v: want %q in %s", tc.mode, tc.want, got)
		}
	}

	// NAMED with an empty tool name can't be encoded — refuse rather
	// than silently falling back to AUTO.
	_, err := toChatRequest("m", &llmv1.CompletionRequest{
		Messages:   base.Messages,
		ToolChoice: &llmv1.ToolChoice{Mode: llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NAMED},
	}, false)
	if err == nil {
		t.Fatal("NAMED with empty named_tool should error, got nil")
	}
}

// A tool-loop history (assistant tool_call, then a tool result)
// replays as OpenAI's native shape: an assistant message with a
// tool_calls array, followed by a role="tool" message keyed by
// tool_call_id — never as an empty multipart message.
func TestToChatRequest_ToolLoopReplay(t *testing.T) {
	req := &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{
			userMsg("list files"),
			toolCallMsg("c1", "Bash", `{"cmd":"ls"}`),
			toolResultMsg("c1", "a.txt\nb.txt"),
		},
	}
	got := mustMarshalRequest(t, req, false)
	if !strings.Contains(got, `"tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{\"cmd\":\"ls\"}"}}]`) {
		t.Fatalf("assistant tool_calls not encoded as expected: %s", got)
	}
	if !strings.Contains(got, `"role":"tool","content":"a.txt\nb.txt","tool_call_id":"c1"`) {
		t.Fatalf("tool role message not encoded as expected: %s", got)
	}
	if strings.Contains(got, `"content":[]`) {
		t.Fatalf("regression: replayed tool history produced an empty multipart message: %s", got)
	}
}

// Tools declared on the request encode with the OpenAI function-tool
// wrapper; an empty parameters schema defaults to an empty object.
func TestToChatRequest_ToolsEncoded(t *testing.T) {
	req := &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{userMsg("hi")},
		Tools: []*llmv1.ToolDeclaration{
			{Name: "Bash", Description: "run a command", ParametersJson: `{"type":"object"}`},
			{Name: "NoSchema"},
		},
	}
	got := mustMarshalRequest(t, req, false)
	if !strings.Contains(got, `"tools":[{"type":"function","function":{"name":"Bash","description":"run a command","parameters":{"type":"object"}}}`) {
		t.Fatalf("tool declaration not encoded as expected: %s", got)
	}
	if !strings.Contains(got, `"name":"NoSchema","parameters":{}`) {
		t.Fatalf("empty parameters schema should default to {}: %s", got)
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

// Two parallel tool calls fragment their arguments across several
// index-keyed deltas; reassembly must not emit anything until [DONE],
// and must preserve call order and per-call argument concatenation.
func TestStream_ToolCallFragmentReassembly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		lines := []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"Bash","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","type":"function","function":{"name":"Grep","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"pattern\":\"x\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]}}]}`,
			`{"choices":[{"finish_reason":"tool_calls"}]}`,
		}
		for _, l := range lines {
			w.Write([]byte("data: " + l + "\n"))
		}
		w.Write([]byte("data: [DONE]\n"))
	}))
	t.Cleanup(srv.Close)

	p := New(Config{BaseURL: srv.URL}).(*provider)
	ci, _ := p.Completer("m")
	c := ci.(*completer)

	var calls []*threadv1.ToolCallContent
	var final *llmv1.StreamChunk
	for chunk, err := range c.Stream(context.Background(), &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{userMsg("hi")},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if tc := chunk.GetToolCall(); tc != nil {
			calls = append(calls, tc)
		}
		if chunk.Done {
			final = chunk
		}
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 reassembled tool calls, got %d: %+v", len(calls), calls)
	}
	if calls[0].Id != "c1" || calls[0].Name != "Bash" || calls[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("first call not reassembled correctly: %+v", calls[0])
	}
	if calls[1].Id != "c2" || calls[1].Name != "Grep" || calls[1].Arguments != `{"pattern":"x"}` {
		t.Fatalf("second call not reassembled correctly: %+v", calls[1])
	}
	if final == nil || final.FinishReason != "tool_calls" {
		t.Fatalf("expected terminal finish_reason=tool_calls, got %+v", final)
	}
}

// Compat endpoints that omit the index field: an id-bearing fragment
// starts a new call, an id-less fragment continues the most recently
// started one.
func TestStream_MissingIndexCompat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		lines := []string{
			`{"choices":[{"delta":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"}"}}]}}]}`,
		}
		for _, l := range lines {
			w.Write([]byte("data: " + l + "\n"))
		}
		w.Write([]byte("data: [DONE]\n"))
	}))
	t.Cleanup(srv.Close)

	p := New(Config{BaseURL: srv.URL}).(*provider)
	ci, _ := p.Completer("m")
	c := ci.(*completer)

	var calls []*threadv1.ToolCallContent
	for chunk, err := range c.Stream(context.Background(), &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{userMsg("hi")},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if tc := chunk.GetToolCall(); tc != nil {
			calls = append(calls, tc)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 reassembled call, got %d: %+v", len(calls), calls)
	}
	if calls[0].Arguments != "{}" {
		t.Fatalf("fragments should concatenate to {}, got %q", calls[0].Arguments)
	}
}

// A continuation fragment (no index, no id) before any call has
// started is a malformed stream — fail loudly rather than guess.
func TestStream_ContinuationBeforeAnyCallErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"x"}}]}}]}` + "\n"))
	}))
	t.Cleanup(srv.Close)

	p := New(Config{BaseURL: srv.URL}).(*provider)
	ci, _ := p.Completer("m")
	c := ci.(*completer)

	var sawErr bool
	for chunk, err := range c.Stream(context.Background(), &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{userMsg("hi")},
	}) {
		if err == nil && chunk != nil && chunk.Error != nil {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("expected a stream error for an orphan continuation fragment")
	}
}

// Non-stream completion decodes finish_reason and falls back to "{}"
// for a tool call with no arguments.
func TestComplete_FinishReasonAndEmptyArgsFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"id": "x", "model": "m",
			"choices": [{
				"message": {"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":""}}]},
				"finish_reason": "tool_calls"
			}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 2}
		}`))
	}))
	t.Cleanup(srv.Close)

	p := New(Config{BaseURL: srv.URL}).(*provider)
	ci, _ := p.Completer("m")
	c := ci.(*completer)

	resp, err := c.Complete(context.Background(), &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{userMsg("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", resp.FinishReason)
	}
	tc := resp.Message.Content[0].GetToolCall()
	if tc == nil || tc.Arguments != "{}" {
		t.Fatalf("expected empty-args fallback to \"{}\", got %+v", tc)
	}
}

// CountText mirrors fromCanonicalMessage: text, tool-call name+args,
// and tool-result content are all sent; thinking is never sent
// (chatwire drops it for this adapter) and must not count.
func TestCountText_IncludesToolContentExcludesThinking(t *testing.T) {
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
	for _, want := range []string{"answer", "Bash", "ARGS", "RESULT"} {
		if !strings.Contains(got, want) {
			t.Fatalf("CountText missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "SECRET") {
		t.Fatalf("CountText includes thinking, which openai never sends: %q", got)
	}
}
