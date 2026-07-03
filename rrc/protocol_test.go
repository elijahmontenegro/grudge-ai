package rrc

import (
	"context"
	"strings"
	"testing"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
)

func storedCall(id, threadID, callID string, position int64) *pb.Message {
	return &pb.Message{
		Id: id, ThreadId: threadID, Position: position, Role: pb.Role_ROLE_ASSISTANT,
		Content: []*pb.ContentBlock{{Block: &pb.ContentBlock_ToolCall{
			ToolCall: &pb.ToolCallContent{Id: callID, Name: "read", Arguments: `{"path":"x"}`},
		}}},
	}
}

func storedResult(id, threadID, callID string, position int64) *pb.Message {
	return &pb.Message{
		Id: id, ThreadId: threadID, Position: position, Role: pb.Role_ROLE_ASSISTANT,
		Content: []*pb.ContentBlock{{Block: &pb.ContentBlock_ToolResult{
			ToolResult: &pb.ToolResultContent{ToolCallId: callID, Content: "body"},
		}}},
	}
}

func TestProtocolClosurePairsExactCounterpart(t *testing.T) {
	call := storedCall("call", "t1", "op-1", 2)
	result := storedResult("result", "t1", "op-1", 3)
	other := storedResult("other", "t1", "op-2", 1)
	index := NewProtocolIndex([]*pb.Message{other, result, call})

	group, err := index.CloseGroup(result, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	if len(group.Messages) != 2 {
		t.Fatalf("closure size=%d, want 2", len(group.Messages))
	}
	if got := group.Messages[0].Content[0].GetToolCall().Id; got != "op-1" {
		t.Fatalf("call id changed or wrong counterpart selected: %q", got)
	}
	if got := group.Messages[1].Content[0].GetToolResult().ToolCallId; got != "op-1" {
		t.Fatalf("result id changed: %q", got)
	}
}

func TestProtocolClosureDoesNotCrossThreads(t *testing.T) {
	call := storedCall("call", "t1", "same-id", 0)
	wrongThread := storedResult("result", "t2", "same-id", 0)
	_, err := NewProtocolIndex([]*pb.Message{call, wrongThread}).CloseGroup(call, 1)
	if err == nil || !strings.Contains(err.Error(), "requires exact tool result") {
		t.Fatalf("expected missing exact counterpart error, got %v", err)
	}
}

func TestProtocolClosureMissingCounterpartFails(t *testing.T) {
	call := storedCall("call", "t1", "missing", 0)
	_, err := NewProtocolIndex([]*pb.Message{call}).CloseGroup(call, 1)
	if err == nil {
		t.Fatal("missing counterpart must be an integrity error")
	}
}

func TestProtocolClosureKeepsWholeStoredMessages(t *testing.T) {
	call := storedCall("call", "t1", "op", 0)
	call.Content = append(call.Content, &pb.ContentBlock{Block: &pb.ContentBlock_Thinking{
		Thinking: &pb.ThinkingContent{Text: "reasoning attached to the call"},
	}})
	result := storedResult("result", "t1", "op", 1)
	group, err := NewProtocolIndex([]*pb.Message{call, result}).CloseGroup(result, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(group.Messages[0].Content) != 2 {
		t.Fatal("closure must restore the canonical stored message, not extract a block")
	}
}

// TestProtocolClosureAmbiguousResultFails: two results claiming the same
// tool_call_id in one thread is a Store-integrity error, not a guess. (The
// index flags it; CloseGroup on the call must refuse rather than bind one.)
func TestProtocolClosureAmbiguousResultFails(t *testing.T) {
	call := storedCall("call", "t1", "op", 0)
	result1 := storedResult("r1", "t1", "op", 1)
	result2 := storedResult("r2", "t1", "op", 2) // duplicate tool_call_id
	index := NewProtocolIndex([]*pb.Message{call, result1, result2})

	_, err := index.CloseGroup(call, 1)
	if err == nil || !strings.Contains(err.Error(), "multiple results") {
		t.Fatalf("ambiguous result must fail as integrity error, got %v", err)
	}
}

// TestProtocolClosureAmbiguousCallFails: symmetric — two calls with the same
// id, closing from the result side.
func TestProtocolClosureAmbiguousCallFails(t *testing.T) {
	call1 := storedCall("c1", "t1", "op", 0)
	call2 := storedCall("c2", "t1", "op", 1) // duplicate call id
	result := storedResult("r", "t1", "op", 2)
	index := NewProtocolIndex([]*pb.Message{call1, call2, result})

	_, err := index.CloseGroup(result, 1)
	if err == nil || !strings.Contains(err.Error(), "multiple calls") {
		t.Fatalf("ambiguous call must fail as integrity error, got %v", err)
	}
}

// TestProtocolClosureBypassesAcceptanceGate is the gate-bypass guarantee: a
// required counterpart travels with its root through Assemble even though, on
// its own, it would never clear the calibrated acceptance floor (it has no
// prerequisite score at all). Protocol integrity is not subject to the
// prerequisite gate — required counterparts are recorded fact.
func TestProtocolClosureBypassesAcceptanceGate(t *testing.T) {
	mc := newMockScorer()
	// The tool-call message is the selected prerequisite (scores high); its
	// result counterpart is never scored (not a prerequisite candidate), so
	// it could only reach the wire via protocol closure, bypassing the gate.
	mc.SetScore("read x", "current context", 0.95)
	o := newMockChunkOracle()
	cfg := DefaultConfig()
	cfg.DiversityLambda = 0
	cfg.MinBatchStdDev = 0
	e := NewEngine(cfg, mc, WithChunkOracle(o))
	ctx := context.Background()

	// A prior tool call (the prerequisite) + its result counterpart.
	call := &pb.Message{
		Id: "mcall", ThreadId: "t1", Position: 0, Role: pb.Role_ROLE_ASSISTANT,
		Content: []*pb.ContentBlock{{Block: &pb.ContentBlock_ToolCall{
			ToolCall: &pb.ToolCallContent{Id: "op", Name: "read", Arguments: "read x"},
		}}},
	}
	o.Register("mcall", "read x")
	result := storedResult("mresult", "t1", "op", 1)
	anchor := addMsg(o, "q", 2, "t1", "current context")
	corpus := []*pb.Message{call, result, anchor}

	res, err := e.Assemble(ctx, AssembleRequest{
		SerializedLocalContext: testSerializedLocalContext(anchor),
		Anchor:                 anchor,
		Corpus:                 corpus,
		LocalContext:           []*pb.Message{anchor},
		Scope:                  pb.SelectionScope_SELECTION_SCOPE_THREAD,
		ThreadID:               "t1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Both the selected call AND its un-scored result counterpart must be on
	// the wire — the counterpart bypassed the acceptance gate via closure.
	var sawCall, sawResult bool
	for _, m := range res.Wire {
		for _, b := range m.Content {
			if tc := b.GetToolCall(); tc != nil && tc.Id == "op" {
				sawCall = true
			}
			if tr := b.GetToolResult(); tr != nil && tr.ToolCallId == "op" {
				sawResult = true
			}
		}
	}
	if !sawCall {
		t.Fatal("selected tool-call prerequisite missing from wire")
	}
	if !sawResult {
		t.Fatal("required result counterpart did not bypass the gate via protocol closure")
	}
}
