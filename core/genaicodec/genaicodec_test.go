package genaicodec

import (
	"testing"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
)

// TestRoundTrip_TextThinkingToolCallToolResult confirms proto→genai→proto
// preserves each content block type and the fields adapters correlate on
// (tool-call id, tool-result id, thinking flag).
func TestRoundTrip_TextThinkingToolCallToolResult(t *testing.T) {
	orig := &pb.LLMMessage{
		Role: pb.Role_ROLE_ASSISTANT,
		Content: []*pb.ContentBlock{
			{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "hello"}}},
			{Block: &pb.ContentBlock_Thinking{Thinking: &pb.ThinkingContent{Text: "reasoning"}}},
			{Block: &pb.ContentBlock_ToolCall{ToolCall: &pb.ToolCallContent{
				Id: "call-1", Name: "read", Arguments: `{"path":"x"}`,
			}}},
			{Block: &pb.ContentBlock_ToolResult{ToolResult: &pb.ToolResultContent{
				ToolCallId: "call-1", Content: `{"answers":{"a":"b"}}`,
			}}},
		},
	}

	got := ContentToProto(ProtoToContent(orig))

	if got.Role != pb.Role_ROLE_ASSISTANT {
		t.Fatalf("role not preserved: %v", got.Role)
	}
	if len(got.Content) != 4 {
		t.Fatalf("block count changed: %d", len(got.Content))
	}
	if got.Content[0].GetText().Text != "hello" {
		t.Fatalf("text block lost: %+v", got.Content[0])
	}
	if got.Content[1].GetThinking().Text != "reasoning" {
		t.Fatalf("thinking block lost: %+v", got.Content[1])
	}
	tc := got.Content[2].GetToolCall()
	if tc == nil || tc.Id != "call-1" || tc.Name != "read" {
		t.Fatalf("tool-call id/name not preserved: %+v", tc)
	}
	tr := got.Content[3].GetToolResult()
	if tr == nil || tr.ToolCallId != "call-1" {
		t.Fatalf("tool-result id not preserved: %+v", tr)
	}
	// The full tool-result JSON must survive (not collapsed to {result:""}).
	if tr.Content == "" || tr.Content == `{"result":""}` {
		t.Fatalf("tool-result content collapsed: %q", tr.Content)
	}
}

// TestRoleMapping confirms the system-role carrier behavior (genai has no
// system role → carried as "user"; the SDK-side system instruction is
// separate).
func TestRoleMapping(t *testing.T) {
	if RoleToGenai(pb.Role_ROLE_SYSTEM) != "user" {
		t.Fatal("system should map to user carrier for genai")
	}
	if RoleToGenai(pb.Role_ROLE_ASSISTANT) != "model" {
		t.Fatal("assistant should map to model")
	}
	if RoleToProto("model") != pb.Role_ROLE_ASSISTANT {
		t.Fatal("model should map to assistant")
	}
}
