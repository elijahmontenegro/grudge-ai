// Package testutil provides shared test fixtures for the service module.
// Keep this light — constructors for pb.Message and pb.LLMMessage with
// the common content-block shapes. Tests that need domain-specific
// helpers (mock classifier, fake completer, fake ADK runner) define
// them in the test file where they're used, since those collaborators
// carry test-specific state.
package testutil

import (
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Msg builds a user-role pb.Message carrying a single text block. The
// canonical corpus-level fixture: message ID, position in thread,
// thread ID, and text. Timestamp defaults to now — tests that need
// ordering control should set Position, not mutate the timestamp.
func Msg(id string, position int64, threadID, text string) *pb.Message {
	return &pb.Message{
		Id:        id,
		Role:      pb.Role_ROLE_USER,
		Content:   []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: text}}}},
		Position:  position,
		ThreadId:  threadID,
		CreatedAt: timestamppb.Now(),
	}
}

// AssistantMsg builds an assistant-role pb.Message with the given blocks.
// Tests assemble tool-chain corpora by interleaving AssistantMsg(toolCall)
// and AssistantMsg(toolResult) entries, since the storage schema is
// one-block-per-message.
func AssistantMsg(id string, position int64, threadID string, blocks ...*pb.ContentBlock) *pb.Message {
	return &pb.Message{
		Id:        id,
		Role:      pb.Role_ROLE_ASSISTANT,
		Content:   blocks,
		Position:  position,
		ThreadId:  threadID,
		CreatedAt: timestamppb.Now(),
	}
}

// TextBlock wraps plain text as a ContentBlock.
func TextBlock(text string) *pb.ContentBlock {
	return &pb.ContentBlock{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: text}}}
}

// ThinkingBlock wraps model reasoning as a ContentBlock.
func ThinkingBlock(text string) *pb.ContentBlock {
	return &pb.ContentBlock{Block: &pb.ContentBlock_Thinking{Thinking: &pb.ThinkingContent{Text: text}}}
}

// ToolCallBlock wraps an assistant-emitted tool invocation.
func ToolCallBlock(callID, name, argsJSON string) *pb.ContentBlock {
	return &pb.ContentBlock{Block: &pb.ContentBlock_ToolCall{ToolCall: &pb.ToolCallContent{
		Id:        callID,
		Name:      name,
		Arguments: argsJSON,
	}}}
}

// ToolResultBlock wraps a tool response bound to its originating call.
func ToolResultBlock(callID, content string) *pb.ContentBlock {
	return &pb.ContentBlock{Block: &pb.ContentBlock_ToolResult{ToolResult: &pb.ToolResultContent{
		ToolCallId: callID,
		Content:    content,
	}}}
}

// LLMText builds a pb.LLMMessage carrying a single text block — the
// minimum wire-shape fixture. Role is caller-supplied so assistant,
// user, and system turns all use the same constructor.
func LLMText(role pb.Role, text string) *pb.LLMMessage {
	return &pb.LLMMessage{
		Role:    role,
		Content: []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: text}}}},
	}
}

// LLMAssistantToolCall builds an assistant-role wire message carrying
// a single tool_call block. Used by protocol tests that simulate
// tool-chain rounds.
func LLMAssistantToolCall(callID, name, argsJSON string) *pb.LLMMessage {
	return &pb.LLMMessage{
		Role:    pb.Role_ROLE_ASSISTANT,
		Content: []*pb.ContentBlock{ToolCallBlock(callID, name, argsJSON)},
	}
}

// LLMToolResult builds an assistant-role wire message carrying a
// tool_result block. Our storage schema places tool results under the
// assistant role (one block per message); adapters remap to "tool"
// role at wire translation time.
func LLMToolResult(callID, content string) *pb.LLMMessage {
	return &pb.LLMMessage{
		Role:    pb.Role_ROLE_ASSISTANT,
		Content: []*pb.ContentBlock{ToolResultBlock(callID, content)},
	}
}

// LLMAssistantThinking builds an assistant-role wire message carrying
// only a thinking block — the shape the ollama canonicalizer must
// merge into the next actionable message rather than emit standalone.
func LLMAssistantThinking(text string) *pb.LLMMessage {
	return &pb.LLMMessage{
		Role:    pb.Role_ROLE_ASSISTANT,
		Content: []*pb.ContentBlock{ThinkingBlock(text)},
	}
}

// LLMMsg builds a pb.LLMMessage from arbitrary blocks — the escape
// hatch when no specific constructor fits (e.g. assistant with
// thinking + text, or parallel tool_calls in one message).
func LLMMsg(role pb.Role, blocks ...*pb.ContentBlock) *pb.LLMMessage {
	return &pb.LLMMessage{Role: role, Content: blocks}
}
