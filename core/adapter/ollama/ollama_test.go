package ollama

import (
	"encoding/json"
	"testing"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
)

// --- Helpers ---

func textBlock(text string) *pb.ContentBlock {
	return &pb.ContentBlock{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: text}}}
}

func thinkingBlock(text string) *pb.ContentBlock {
	return &pb.ContentBlock{Block: &pb.ContentBlock_Thinking{Thinking: &pb.ThinkingContent{Text: text}}}
}

func toolCallBlock(id, name, args string) *pb.ContentBlock {
	return &pb.ContentBlock{Block: &pb.ContentBlock_ToolCall{ToolCall: &pb.ToolCallContent{
		Id: id, Name: name, Arguments: args,
	}}}
}

func toolResultBlock(callID, content string) *pb.ContentBlock {
	return &pb.ContentBlock{Block: &pb.ContentBlock_ToolResult{ToolResult: &pb.ToolResultContent{
		ToolCallId: callID, Content: content,
	}}}
}

func msg(role pb.Role, blocks ...*pb.ContentBlock) *pb.LLMMessage {
	return &pb.LLMMessage{Role: role, Content: blocks}
}

// --- Tests ---

func TestToLlamaMsgs_PairedToolCallAndResult(t *testing.T) {
	// Base case: assistant emits a tool_call, tool responds, model
	// continues. Expected wire shape: assistant message carrying the
	// tool_calls array, followed by a role=tool message keyed to the
	// call id.
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_ASSISTANT, toolCallBlock("c1", "Bash", `{"cmd":"ls"}`)),
		msg(pb.Role_ROLE_ASSISTANT, toolResultBlock("c1", "a.txt\nb.txt")),
	}
	out := toLlamaMsgs(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	if out[0].Role != "assistant" || len(out[0].ToolCalls) != 1 {
		t.Fatalf("first must be assistant with 1 tool_call, got %+v", out[0])
	}
	if out[0].ToolCalls[0].ID != "c1" {
		t.Fatalf("tool_call id mismatch: %s", out[0].ToolCalls[0].ID)
	}
	if out[1].Role != "tool" || out[1].ToolCallID != "c1" || out[1].Content != "a.txt\nb.txt" {
		t.Fatalf("second must be tool response for c1, got %+v", out[1])
	}
}

func TestToLlamaMsgs_OrphanToolCallDropped(t *testing.T) {
	// tool_call without a matching tool_result atom — the provider
	// would reject this as protocol-malformed. Drop it.
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_ASSISTANT, toolCallBlock("c1", "Bash", `{}`)),
	}
	out := toLlamaMsgs(in)
	if len(out) != 0 {
		t.Fatalf("orphan tool_call should produce no output, got %d messages", len(out))
	}
}

func TestToLlamaMsgs_OrphanToolResultDropped(t *testing.T) {
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_ASSISTANT, toolResultBlock("c1", "orphan")),
	}
	out := toLlamaMsgs(in)
	if len(out) != 0 {
		t.Fatalf("orphan tool_result should produce no output, got %d", len(out))
	}
}

func TestToLlamaMsgs_ConsecutiveToolCallsMerged(t *testing.T) {
	// ADK splits parallel tool calls into separate events; our
	// storage captures each as its own pb.LLMMessage. The wire needs
	// them grouped into one assistant{tool_calls:[...]} entry.
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_ASSISTANT, toolCallBlock("c1", "Bash", `{}`)),
		msg(pb.Role_ROLE_ASSISTANT, toolCallBlock("c2", "Grep", `{}`)),
		msg(pb.Role_ROLE_ASSISTANT, toolResultBlock("c1", "out1")),
		msg(pb.Role_ROLE_ASSISTANT, toolResultBlock("c2", "out2")),
	}
	out := toLlamaMsgs(in)
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

func TestToLlamaMsgs_ThinkingOnlyMergedIntoNextAction(t *testing.T) {
	// A standalone thinking-only assistant violates the OpenAI tool
	// protocol (assistant must have content or tool_calls). The
	// canonicalizer buffers it and merges into the next actionable
	// assistant message.
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_ASSISTANT, thinkingBlock("I should search for the file")),
		msg(pb.Role_ROLE_ASSISTANT, toolCallBlock("c1", "Grep", `{}`)),
		msg(pb.Role_ROLE_ASSISTANT, toolResultBlock("c1", "found")),
	}
	out := toLlamaMsgs(in)
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

func TestToLlamaMsgs_TrailingThinkingDropped(t *testing.T) {
	// Thinking atom with no subsequent action gets dropped — emitting
	// a trailing thinking-only assistant would be the exact protocol
	// violation the canonicalizer exists to prevent.
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_USER, textBlock("hi")),
		msg(pb.Role_ROLE_ASSISTANT, thinkingBlock("about to think...")),
	}
	out := toLlamaMsgs(in)
	if len(out) != 1 {
		t.Fatalf("trailing thinking should drop, got %d", len(out))
	}
	if out[0].Role != "user" || out[0].Content != "hi" {
		t.Fatalf("only the user message should survive, got %+v", out[0])
	}
}

func TestToLlamaMsgs_TextWithThinkingPreservesBoth(t *testing.T) {
	// When thinking and text coexist in a single pb.LLMMessage, both
	// land on the same emitted chatMessage.
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_ASSISTANT, thinkingBlock("reasoning"), textBlock("response")),
	}
	out := toLlamaMsgs(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Content != "response" || out[0].Thinking != "reasoning" {
		t.Fatalf("content or thinking lost: %+v", out[0])
	}
}

func TestToLlamaMsgs_ParallelToolCallsInSameMessage(t *testing.T) {
	// A single pb.LLMMessage carrying multiple tool_call blocks
	// (parallel invocation in one ADK event) becomes one emitted
	// assistant with both calls in the tool_calls array.
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_ASSISTANT,
			toolCallBlock("c1", "A", `{}`),
			toolCallBlock("c2", "B", `{}`),
		),
		msg(pb.Role_ROLE_ASSISTANT, toolResultBlock("c1", "a")),
		msg(pb.Role_ROLE_ASSISTANT, toolResultBlock("c2", "b")),
	}
	out := toLlamaMsgs(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out))
	}
	if len(out[0].ToolCalls) != 2 {
		t.Fatalf("parallel calls should group, got %d", len(out[0].ToolCalls))
	}
}

func TestToLlamaMsgs_EmptyTextAndThinkingAtomDropped(t *testing.T) {
	// A pb.LLMMessage with only a text block whose Text is "" produces
	// an atom with text="" and thinking="". Dropped silently.
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_USER, textBlock("real")),
		msg(pb.Role_ROLE_ASSISTANT, textBlock("")),
	}
	out := toLlamaMsgs(in)
	if len(out) != 1 {
		t.Fatalf("empty atom should drop silently, got %d", len(out))
	}
	if out[0].Role != "user" || out[0].Content != "real" {
		t.Fatalf("wrong message survived: %+v", out[0])
	}
}

func TestToLlamaMsgs_SystemPassthrough(t *testing.T) {
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_SYSTEM, textBlock("You are a helpful assistant.")),
		msg(pb.Role_ROLE_USER, textBlock("hi")),
	}
	out := toLlamaMsgs(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	if out[0].Role != "system" || out[0].Content != "You are a helpful assistant." {
		t.Fatalf("system message wrong: %+v", out[0])
	}
	if out[1].Role != "user" || out[1].Content != "hi" {
		t.Fatalf("user message wrong: %+v", out[1])
	}
}

func TestToLlamaMsgs_OrphanCallMixedWithPaired(t *testing.T) {
	// Two tool_calls issued consecutively; only one has a matching
	// result. The paired one forms the group, the orphan is dropped,
	// and the stray result (for a call that was never kept) is
	// dropped too. Regression guard for the "assistant with all-orphan
	// tool_calls" protocol violation.
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_ASSISTANT, toolCallBlock("c1", "A", `{}`)),
		msg(pb.Role_ROLE_ASSISTANT, toolCallBlock("c2", "B", `{}`)),
		msg(pb.Role_ROLE_ASSISTANT, toolResultBlock("c1", "out1")),
		// c3 has no preceding call — orphan result
		msg(pb.Role_ROLE_ASSISTANT, toolResultBlock("c3", "out3")),
	}
	out := toLlamaMsgs(in)
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

func TestToLlamaMsgs_TextAndToolCallInSameMessage(t *testing.T) {
	// A pb.LLMMessage with text + tool_call produces two atoms
	// (text first, then toolcall). Both emit as separate wire
	// messages: the text as a role=assistant content message, then
	// the tool_call group as its own assistant message.
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_ASSISTANT,
			textBlock("let me check"),
			toolCallBlock("c1", "Grep", `{}`),
		),
		msg(pb.Role_ROLE_ASSISTANT, toolResultBlock("c1", "out")),
	}
	out := toLlamaMsgs(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages (text, assistant{tool_calls}, tool), got %d", len(out))
	}
	if out[0].Content != "let me check" {
		t.Fatalf("first message should carry the text, got %+v", out[0])
	}
	if len(out[1].ToolCalls) != 1 {
		t.Fatalf("second message should carry the tool_call, got %+v", out[1])
	}
	if out[2].ToolCallID != "c1" {
		t.Fatalf("third should be the tool response, got %+v", out[2])
	}
}

func TestToLlamaMsgs_ThinkingBetweenTwoToolCallGroups(t *testing.T) {
	// Two tool_call groups separated by a thinking-only atom. The
	// thinking should merge into the SECOND group's assistant message
	// (the next actionable after the thinking), not the first.
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_ASSISTANT, toolCallBlock("c1", "A", `{}`)),
		msg(pb.Role_ROLE_ASSISTANT, toolResultBlock("c1", "a")),
		msg(pb.Role_ROLE_ASSISTANT, thinkingBlock("let me try B")),
		msg(pb.Role_ROLE_ASSISTANT, toolCallBlock("c2", "B", `{}`)),
		msg(pb.Role_ROLE_ASSISTANT, toolResultBlock("c2", "b")),
	}
	out := toLlamaMsgs(in)
	if len(out) != 4 {
		t.Fatalf("expected 4 wire messages, got %d", len(out))
	}
	if out[0].Thinking != "" {
		t.Fatalf("first assistant group should have empty thinking, got %q", out[0].Thinking)
	}
	// out[2] is the second assistant{tool_calls}, should carry the thinking.
	if out[2].Thinking != "let me try B" {
		t.Fatalf("thinking should merge into the NEXT tool_call group, got %q", out[2].Thinking)
	}
}

func TestToLlamaMsgs_ArgumentsPreserveRawJSON(t *testing.T) {
	// Tool call arguments are stored as a JSON string in
	// ToolCallContent.Arguments. The canonicalizer must pass them
	// through as json.RawMessage so the wire encodes them as a JSON
	// object, not a double-encoded string.
	argsJSON := `{"pattern":"*.go","path":"/workspace"}`
	in := []*pb.LLMMessage{
		msg(pb.Role_ROLE_ASSISTANT, toolCallBlock("c1", "Glob", argsJSON)),
		msg(pb.Role_ROLE_ASSISTANT, toolResultBlock("c1", "out")),
	}
	out := toLlamaMsgs(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	// Marshal the whole message and verify the arguments land as an
	// object literal, not a quoted string.
	b, err := json.Marshal(out[0])
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstring(string(b), `"arguments":{"pattern":"*.go","path":"/workspace"}`) {
		t.Fatalf("arguments should encode as a JSON object literal, got: %s", string(b))
	}
}

// containsSubstring is a tiny helper — strings.Contains equivalent
// kept local to avoid an import just for one call.
func containsSubstring(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
