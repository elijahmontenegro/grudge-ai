package anthropic

import (
	"context"
	"encoding/json"
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

func toolCallMsg(id, name, args string) *llmv1.LLMMessage {
	return &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{Id: id, Name: name, Arguments: args}}},
		},
	}
}

func toolResultMsg(id, content string, isError bool) *llmv1.LLMMessage {
	return &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{ToolCallId: id, Content: content, IsError: isError}}},
		},
	}
}

func mustEncodeRequest(t *testing.T, req *llmv1.CompletionRequest, stream bool) string {
	t.Helper()
	areq, err := toAPIRequest("claude-test", req, stream)
	if err != nil {
		t.Fatalf("toAPIRequest: %v", err)
	}
	b, err := json.Marshal(areq)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":42}}\n"))
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
	if final.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want stop (end_turn normalized)", final.FinishReason)
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

// All four ToolChoice modes encode to Anthropic's documented shapes;
// unset/UNSPECIFIED omits the field entirely.
func TestToAPIRequest_ToolChoiceModes(t *testing.T) {
	base := &llmv1.CompletionRequest{Messages: []*llmv1.LLMMessage{userMsg("hi")}}

	if got := mustEncodeRequest(t, base, false); strings.Contains(got, "tool_choice") {
		t.Fatalf("unset ToolChoice must omit the field, got: %s", got)
	}

	cases := []struct {
		mode llmv1.ToolChoiceMode
		name string
		want string
	}{
		{llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_AUTO, "", `"tool_choice":{"type":"auto"}`},
		{llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NONE, "", `"tool_choice":{"type":"none"}`},
		{llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_REQUIRED, "", `"tool_choice":{"type":"any"}`},
		{llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NAMED, "Bash", `"tool_choice":{"type":"tool","name":"Bash"}`},
	}
	for _, tc := range cases {
		req := &llmv1.CompletionRequest{
			Messages:   base.Messages,
			ToolChoice: &llmv1.ToolChoice{Mode: tc.mode, NamedTool: tc.name},
		}
		got := mustEncodeRequest(t, req, false)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("mode %v: want %q in %s", tc.mode, tc.want, got)
		}
	}

	_, err := toAPIRequest("claude-test", &llmv1.CompletionRequest{
		Messages:   base.Messages,
		ToolChoice: &llmv1.ToolChoice{Mode: llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NAMED},
	}, false)
	if err == nil {
		t.Fatal("NAMED with empty named_tool should error, got nil")
	}
}

// Tools declared on the request encode with Anthropic's input_schema
// wrapper; an empty parameters schema defaults to an object schema
// (Anthropic rejects a missing schema).
func TestToAPIRequest_ToolsEncoded(t *testing.T) {
	req := &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{userMsg("hi")},
		Tools: []*llmv1.ToolDeclaration{
			{Name: "Bash", Description: "run a command", ParametersJson: `{"type":"object","properties":{}}`},
			{Name: "NoSchema"},
		},
	}
	got := mustEncodeRequest(t, req, false)
	if !strings.Contains(got, `"tools":[{"name":"Bash","description":"run a command","input_schema":{"type":"object","properties":{}}}`) {
		t.Fatalf("tool declaration not encoded as expected: %s", got)
	}
	if !strings.Contains(got, `"name":"NoSchema","input_schema":{"type":"object"}`) {
		t.Fatalf("empty parameters schema should default to an object schema: %s", got)
	}
}

// Regression: tool_use.input must be a JSON object on the wire, not a
// double-encoded string — Anthropic rejects a string input.
func TestToAPIContent_ToolUseInputIsJSONObject(t *testing.T) {
	req := &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{toolCallMsg("c1", "Glob", `{"pattern":"*.go"}`)},
	}
	got := mustEncodeRequest(t, req, false)
	if !strings.Contains(got, `"input":{"pattern":"*.go"}`) {
		t.Fatalf(`tool_use.input must encode as an object, got: %s`, got)
	}
	if strings.Contains(got, `"input":"{`) {
		t.Fatalf("regression: tool_use.input encoded as a double-quoted string: %s", got)
	}
}

// A tool_result block replays with is_error carried, and a tool_call
// with empty arguments normalizes to an empty object (never a
// null/empty RawMessage, which would produce invalid JSON).
func TestToAPIContent_ToolResultIsErrorAndEmptyArgsFallback(t *testing.T) {
	req := &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{
			toolCallMsg("c1", "Bash", ""),
			toolResultMsg("c1", "boom", true),
		},
	}
	got := mustEncodeRequest(t, req, false)
	if !strings.Contains(got, `"input":{}`) {
		t.Fatalf("empty tool_call arguments should normalize to {}, got: %s", got)
	}
	if !strings.Contains(got, `"is_error":true`) {
		t.Fatalf("tool_result is_error not replayed: %s", got)
	}
}

// A thinking block replays ONLY when signed — Anthropic rejects an
// unsigned thinking block, so encoding one would break every request
// that happens to carry historical unsigned thinking.
func TestToAPIContent_ThinkingOnlyReplayedWhenSigned(t *testing.T) {
	unsigned := &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "unsigned reasoning"}}},
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "answer"}}},
		},
	}
	got := mustEncodeRequest(t, &llmv1.CompletionRequest{Messages: []*llmv1.LLMMessage{unsigned}}, false)
	if strings.Contains(got, "unsigned reasoning") {
		t.Fatalf("unsigned thinking must never be replayed: %s", got)
	}

	signed := &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "signed reasoning", Signature: []byte("sig-bytes")}}},
		},
	}
	got = mustEncodeRequest(t, &llmv1.CompletionRequest{Messages: []*llmv1.LLMMessage{signed}}, false)
	if !strings.Contains(got, `"type":"thinking","thinking":"signed reasoning","signature":"sig-bytes"`) {
		t.Fatalf("signed thinking should replay with its text and signature: %s", got)
	}
}

// Non-stream decode: tool_use content parts become ToolCallContent
// (previously dropped entirely), and thinking is read from the
// "thinking" field with its signature captured — the field-name bug
// meant thinking decode was silently broken before this fix.
func TestFromAPIContent_DecodesToolUseAndThinkingWithSignature(t *testing.T) {
	c := testCompleter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"id": "msg_1", "model": "claude-test",
			"content": [
				{"type":"thinking","thinking":"reasoning text","signature":"abc123"},
				{"type":"tool_use","id":"c1","name":"Bash","input":{"cmd":"ls"}},
				{"type":"tool_use","id":"c2","name":"NoArgs","input":{}}
			],
			"usage": {"input_tokens":1,"output_tokens":1},
			"stop_reason": "tool_use"
		}`))
	})

	resp, err := c.Complete(context.Background(), &llmv1.CompletionRequest{Messages: []*llmv1.LLMMessage{userMsg("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.Message.Content) != 3 {
		t.Fatalf("expected 3 decoded blocks, got %d: %+v", len(resp.Message.Content), resp.Message.Content)
	}
	think := resp.Message.Content[0].GetThinking()
	if think == nil || think.Text != "reasoning text" || string(think.Signature) != "abc123" {
		t.Fatalf("thinking not decoded from the \"thinking\" field with signature: %+v", think)
	}
	tc1 := resp.Message.Content[1].GetToolCall()
	if tc1 == nil || tc1.Id != "c1" || tc1.Name != "Bash" || tc1.Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool_use not decoded: %+v", tc1)
	}
	tc2 := resp.Message.Content[2].GetToolCall()
	if tc2 == nil || tc2.Arguments != "{}" {
		t.Fatalf("empty tool_use input should decode as {}, got: %+v", tc2)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls (stop_reason tool_use normalized)", resp.FinishReason)
	}
}

// SSE tool_use reassembly: content_block_start announces the block,
// input_json_delta fragments accumulate, content_block_stop emits the
// completed call. A concurrent text block at another index is
// unaffected.
func TestStream_ToolUseBlockReassembly(t *testing.T) {
	c := testCompleter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		lines := []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"checking"}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"c1","name":"Bash"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		}
		for _, l := range lines {
			w.Write([]byte("data: " + l + "\n"))
		}
	})

	var text strings.Builder
	var calls []*threadv1.ToolCallContent
	var final *llmv1.StreamChunk
	for chunk, err := range c.Stream(context.Background(), &llmv1.CompletionRequest{Messages: []*llmv1.LLMMessage{userMsg("hi")}}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if tb := chunk.GetText(); tb != nil {
			text.WriteString(tb.Text)
		}
		if tc := chunk.GetToolCall(); tc != nil {
			calls = append(calls, tc)
		}
		if chunk.Done {
			final = chunk
		}
	}
	if text.String() != "checking" {
		t.Fatalf("text block interleaved with tool_use lost: %q", text.String())
	}
	if len(calls) != 1 || calls[0].Id != "c1" || calls[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool_use not reassembled correctly: %+v", calls)
	}
	if final == nil || final.FinishReason != "tool_calls" {
		t.Fatalf("expected terminal finish_reason=tool_calls, got %+v", final)
	}
}

// signature_delta arrives after the thinking text and before the
// block's content_block_stop; the adapter emits a dedicated zero-text
// thinking chunk carrying only the signature — the runner treats it
// as "attach this signature," not as more text.
func TestStream_SignatureDeltaEmitsZeroTextThinkingChunk(t *testing.T) {
	c := testCompleter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		lines := []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me check"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-xyz"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		}
		for _, l := range lines {
			w.Write([]byte("data: " + l + "\n"))
		}
	})

	var thinkingChunks []*threadv1.ThinkingContent
	for chunk, err := range c.Stream(context.Background(), &llmv1.CompletionRequest{Messages: []*llmv1.LLMMessage{userMsg("hi")}}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if th := chunk.GetThinking(); th != nil {
			thinkingChunks = append(thinkingChunks, th)
		}
	}
	if len(thinkingChunks) != 2 {
		t.Fatalf("expected 2 thinking chunks (text + signature terminator), got %d: %+v", len(thinkingChunks), thinkingChunks)
	}
	if thinkingChunks[0].Text != "let me check" || len(thinkingChunks[0].Signature) != 0 {
		t.Fatalf("first thinking chunk should carry text with no signature: %+v", thinkingChunks[0])
	}
	if thinkingChunks[1].Text != "" || string(thinkingChunks[1].Signature) != "sig-xyz" {
		t.Fatalf("second thinking chunk should be zero-text carrying the signature: %+v", thinkingChunks[1])
	}
}

// A thinking block that closes WITHOUT a signature_delta emits no
// terminator chunk at all — there is nothing to attach.
func TestStream_UnsignedThinkingBlockEmitsNoTerminator(t *testing.T) {
	c := testCompleter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		lines := []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"unsigned"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		}
		for _, l := range lines {
			w.Write([]byte("data: " + l + "\n"))
		}
	})

	var thinkingChunks []*threadv1.ThinkingContent
	for chunk, err := range c.Stream(context.Background(), &llmv1.CompletionRequest{Messages: []*llmv1.LLMMessage{userMsg("hi")}}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if th := chunk.GetThinking(); th != nil {
			thinkingChunks = append(thinkingChunks, th)
		}
	}
	if len(thinkingChunks) != 1 {
		t.Fatalf("expected only the text-bearing thinking chunk, got %d: %+v", len(thinkingChunks), thinkingChunks)
	}
}

// CountText mirrors toAPIContent: text + tool_use + tool_result are
// sent; thinking is counted only when signed (mirroring the
// signed-only replay rule) and attachments are never sent.
func TestCountText_MirrorsToAPIContent(t *testing.T) {
	m := &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "answer"}}},
			{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "UNSIGNED-REASONING"}}},
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
	if strings.Contains(got, "UNSIGNED-REASONING") {
		t.Fatalf("CountText includes unsigned thinking, which anthropic never replays: %q", got)
	}

	signed := &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "SIGNED-REASONING", Signature: []byte("sig")}}},
		},
	}
	if got := CountText(signed); !strings.Contains(got, "SIGNED-REASONING") {
		t.Fatalf("CountText should include signed thinking (it will be replayed): %q", got)
	}
}
