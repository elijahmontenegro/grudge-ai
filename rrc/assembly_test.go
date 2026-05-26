package rrc

import (
	"testing"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// hasTextBlock guards dynamical Radius reachback (Phase B.2). The
// invariant: only externalized user / assistant text counts as a
// conversational anchor. Tool plumbing and internal thinking do not.
// A break here lets a deep tool loop hide the most-recent real reply.

func TestHasTextBlock_TextOnly(t *testing.T) {
	blocks := []*pb.ContentBlock{
		{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "hello"}}},
	}
	if !hasTextBlock(blocks) {
		t.Error("expected hasTextBlock=true for plain text content")
	}
}

func TestHasTextBlock_EmptyText(t *testing.T) {
	blocks := []*pb.ContentBlock{
		{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: ""}}},
	}
	if hasTextBlock(blocks) {
		t.Error("empty text block should not qualify as semantic content")
	}
}

func TestHasTextBlock_ToolOnlyMessage(t *testing.T) {
	toolCall := []*pb.ContentBlock{
		{Block: &pb.ContentBlock_ToolCall{ToolCall: &pb.ToolCallContent{
			Id: "t1", Name: "Read", Arguments: `{"path":"x"}`,
		}}},
	}
	if hasTextBlock(toolCall) {
		t.Error("tool_call-only content must not qualify as an anchor")
	}
	toolResult := []*pb.ContentBlock{
		{Block: &pb.ContentBlock_ToolResult{ToolResult: &pb.ToolResultContent{
			ToolCallId: "t1", Content: "file contents here",
		}}},
	}
	if hasTextBlock(toolResult) {
		t.Error("tool_result-only content must not qualify as an anchor")
	}
}

func TestHasTextBlock_ThinkingOnly(t *testing.T) {
	blocks := []*pb.ContentBlock{
		{Block: &pb.ContentBlock_Thinking{Thinking: &pb.ThinkingContent{Text: "deliberating..."}}},
	}
	if hasTextBlock(blocks) {
		t.Error("thinking-only content must not qualify as a conversational anchor")
	}
}

func TestHasTextBlock_MixedPrefersText(t *testing.T) {
	blocks := []*pb.ContentBlock{
		{Block: &pb.ContentBlock_Thinking{Thinking: &pb.ThinkingContent{Text: "deliberating..."}}},
		{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "here's my reply"}}},
	}
	if !hasTextBlock(blocks) {
		t.Error("thinking+text content should qualify via the text block")
	}
}
