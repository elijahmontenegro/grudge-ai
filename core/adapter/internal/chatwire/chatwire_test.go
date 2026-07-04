package chatwire

import (
	"testing"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

func textBlock(text string) *threadv1.ContentBlock {
	return &threadv1.ContentBlock{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: text}}}
}

func thinkingBlock(text string) *threadv1.ContentBlock {
	return &threadv1.ContentBlock{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: text}}}
}

func toolCallBlock(id, name, args string) *threadv1.ContentBlock {
	return &threadv1.ContentBlock{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{
		Id: id, Name: name, Arguments: args,
	}}}
}

func toolResultBlock(callID, content string) *threadv1.ContentBlock {
	return &threadv1.ContentBlock{Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{
		ToolCallId: callID, Content: content,
	}}}
}

func msg(role threadv1.Role, blocks ...*threadv1.ContentBlock) *llmv1.LLMMessage {
	return &llmv1.LLMMessage{Role: role, Content: blocks}
}

func TestCanonicalize_PairedToolCallAndResult(t *testing.T) {
	in := []*llmv1.LLMMessage{
		msg(threadv1.Role_ROLE_ASSISTANT, toolCallBlock("c1", "Bash", `{"cmd":"ls"}`)),
		msg(threadv1.Role_ROLE_ASSISTANT, toolResultBlock("c1", "a.txt\nb.txt")),
	}
	out := Canonicalize(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	if out[0].Role != "assistant" || len(out[0].ToolCalls) != 1 {
		t.Fatalf("first must be assistant with 1 tool_call, got %+v", out[0])
	}
	if out[0].ToolCalls[0].ID != "c1" || out[0].ToolCalls[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool_call mismatch: %+v", out[0].ToolCalls[0])
	}
	if out[1].Role != "tool" || out[1].ToolCallID != "c1" || out[1].Text != "a.txt\nb.txt" {
		t.Fatalf("second must be tool response for c1, got %+v", out[1])
	}
}

func TestCanonicalize_OrphanToolCallDropped(t *testing.T) {
	in := []*llmv1.LLMMessage{
		msg(threadv1.Role_ROLE_ASSISTANT, toolCallBlock("c1", "Bash", `{}`)),
	}
	if out := Canonicalize(in); len(out) != 0 {
		t.Fatalf("orphan tool_call should produce no output, got %d messages", len(out))
	}
}

func TestCanonicalize_OrphanToolResultDropped(t *testing.T) {
	in := []*llmv1.LLMMessage{
		msg(threadv1.Role_ROLE_ASSISTANT, toolResultBlock("c1", "orphan")),
	}
	if out := Canonicalize(in); len(out) != 0 {
		t.Fatalf("orphan tool_result should produce no output, got %d", len(out))
	}
}

func TestCanonicalize_ConsecutiveToolCallsMerged(t *testing.T) {
	in := []*llmv1.LLMMessage{
		msg(threadv1.Role_ROLE_ASSISTANT, toolCallBlock("c1", "Bash", `{}`)),
		msg(threadv1.Role_ROLE_ASSISTANT, toolCallBlock("c2", "Grep", `{}`)),
		msg(threadv1.Role_ROLE_ASSISTANT, toolResultBlock("c1", "out1")),
		msg(threadv1.Role_ROLE_ASSISTANT, toolResultBlock("c2", "out2")),
	}
	out := Canonicalize(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages (1 merged assistant + 2 tools), got %d", len(out))
	}
	if out[0].Role != "assistant" || len(out[0].ToolCalls) != 2 {
		t.Fatalf("expected merged assistant with 2 tool_calls, got %+v", out[0])
	}
	if out[0].ToolCalls[0].ID != "c1" || out[0].ToolCalls[1].ID != "c2" {
		t.Fatal("tool_call ids out of order")
	}
	if out[1].ToolCallID != "c1" || out[2].ToolCallID != "c2" {
		t.Fatal("tool responses out of order")
	}
}

func TestCanonicalize_ThinkingOnlyMergedIntoNextAction(t *testing.T) {
	in := []*llmv1.LLMMessage{
		msg(threadv1.Role_ROLE_ASSISTANT, thinkingBlock("I should search for the file")),
		msg(threadv1.Role_ROLE_ASSISTANT, toolCallBlock("c1", "Grep", `{}`)),
		msg(threadv1.Role_ROLE_ASSISTANT, toolResultBlock("c1", "found")),
	}
	out := Canonicalize(in)
	if len(out) != 2 {
		t.Fatalf("thinking-only atom should NOT produce its own message, got %d", len(out))
	}
	if out[0].Role != "assistant" || len(out[0].ToolCalls) != 1 {
		t.Fatalf("thinking should be merged into assistant{tool_calls}, got %+v", out[0])
	}
	if out[0].Thinking != "I should search for the file" {
		t.Fatalf("thinking not preserved on merge: %q", out[0].Thinking)
	}
}

func TestCanonicalize_TrailingThinkingDropped(t *testing.T) {
	in := []*llmv1.LLMMessage{
		msg(threadv1.Role_ROLE_USER, textBlock("hi")),
		msg(threadv1.Role_ROLE_ASSISTANT, thinkingBlock("about to think...")),
	}
	out := Canonicalize(in)
	if len(out) != 1 {
		t.Fatalf("trailing thinking should drop, got %d", len(out))
	}
	if out[0].Role != "user" || out[0].Text != "hi" {
		t.Fatalf("only the user message should survive, got %+v", out[0])
	}
}

func TestCanonicalize_TextWithThinkingPreservesBoth(t *testing.T) {
	in := []*llmv1.LLMMessage{
		msg(threadv1.Role_ROLE_ASSISTANT, thinkingBlock("reasoning"), textBlock("response")),
	}
	out := Canonicalize(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Text != "response" || out[0].Thinking != "reasoning" {
		t.Fatalf("content or thinking lost: %+v", out[0])
	}
}

func TestCanonicalize_OrphanCallMixedWithPaired(t *testing.T) {
	in := []*llmv1.LLMMessage{
		msg(threadv1.Role_ROLE_ASSISTANT, toolCallBlock("c1", "A", `{}`)),
		msg(threadv1.Role_ROLE_ASSISTANT, toolCallBlock("c2", "B", `{}`)),
		msg(threadv1.Role_ROLE_ASSISTANT, toolResultBlock("c1", "out1")),
		msg(threadv1.Role_ROLE_ASSISTANT, toolResultBlock("c3", "out3")),
	}
	out := Canonicalize(in)
	if len(out) != 2 {
		t.Fatalf("expected [assistant{c1}, tool{c1}], got %d messages", len(out))
	}
	if len(out[0].ToolCalls) != 1 || out[0].ToolCalls[0].ID != "c1" {
		t.Fatalf("only c1 should be kept, got %+v", out[0].ToolCalls)
	}
	if out[1].ToolCallID != "c1" {
		t.Fatalf("expected tool response for c1, got %+v", out[1])
	}
}

func TestCanonicalize_ThinkingBetweenTwoToolCallGroups(t *testing.T) {
	in := []*llmv1.LLMMessage{
		msg(threadv1.Role_ROLE_ASSISTANT, toolCallBlock("c1", "A", `{}`)),
		msg(threadv1.Role_ROLE_ASSISTANT, toolResultBlock("c1", "a")),
		msg(threadv1.Role_ROLE_ASSISTANT, thinkingBlock("let me try B")),
		msg(threadv1.Role_ROLE_ASSISTANT, toolCallBlock("c2", "B", `{}`)),
		msg(threadv1.Role_ROLE_ASSISTANT, toolResultBlock("c2", "b")),
	}
	out := Canonicalize(in)
	if len(out) != 4 {
		t.Fatalf("expected 4 wire messages, got %d", len(out))
	}
	if out[0].Thinking != "" {
		t.Fatalf("first assistant group should have empty thinking, got %q", out[0].Thinking)
	}
	if out[2].Thinking != "let me try B" {
		t.Fatalf("thinking should merge into the NEXT tool_call group, got %q", out[2].Thinking)
	}
}

func TestCanonicalize_EmptyArgsNormalizedToEmptyObject(t *testing.T) {
	in := []*llmv1.LLMMessage{
		msg(threadv1.Role_ROLE_ASSISTANT, toolCallBlock("c1", "Bash", "")),
		msg(threadv1.Role_ROLE_ASSISTANT, toolResultBlock("c1", "out")),
	}
	out := Canonicalize(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	if out[0].ToolCalls[0].Arguments != "{}" {
		t.Fatalf("empty arguments should normalize to \"{}\", got %q", out[0].ToolCalls[0].Arguments)
	}
}

func TestCanonicalize_SystemPassthrough(t *testing.T) {
	in := []*llmv1.LLMMessage{
		msg(threadv1.Role_ROLE_SYSTEM, textBlock("You are a helpful assistant.")),
		msg(threadv1.Role_ROLE_USER, textBlock("hi")),
	}
	out := Canonicalize(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	if out[0].Role != "system" || out[0].Text != "You are a helpful assistant." {
		t.Fatalf("system message wrong: %+v", out[0])
	}
	if out[1].Role != "user" || out[1].Text != "hi" {
		t.Fatalf("user message wrong: %+v", out[1])
	}
}
