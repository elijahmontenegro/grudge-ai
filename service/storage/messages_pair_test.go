package storage

import (
	"testing"

	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestInsertToolCallPair_AtomicOnFailure verifies the core write-side
// guarantee: if either row of the (call, result) pair fails to insert,
// NEITHER is committed — so a lone tool_call can never reach the corpus.
// A lone call bricks rrc/protocol.go CloseGroup on every later assembly.
func TestInsertToolCallPair_AtomicOnFailure(t *testing.T) {
	db := testDB(t)
	if err := db.CreateThread(&threadv1.Thread{Id: "t1", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	// Pre-insert a message whose id the pair's result will collide with,
	// forcing the SECOND insert of the pair to fail on the primary key.
	if err := db.InsertMessage(&threadv1.Message{
		Id: "dup", ThreadId: "t1", Role: threadv1.Role_ROLE_ASSISTANT, Position: 0,
	}, nil); err != nil {
		t.Fatal(err)
	}

	call := &threadv1.Message{
		Id: "call-row", ThreadId: "t1", Role: threadv1.Role_ROLE_ASSISTANT, Position: 1,
		Content: []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_ToolCall{
			ToolCall: &threadv1.ToolCallContent{Id: "x", Name: "Bash"},
		}}},
	}
	result := &threadv1.Message{
		Id: "dup", ThreadId: "t1", Role: threadv1.Role_ROLE_ASSISTANT, Position: 2, // id collides
		Content: []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_ToolResult{
			ToolResult: &threadv1.ToolResultContent{ToolCallId: "x", Content: "out"},
		}}},
	}

	if err := db.InsertToolCallPair(call, nil, result, nil); err == nil {
		t.Fatal("expected the pair insert to fail on the colliding result id")
	}
	// The call row must have rolled back with the failed result — both or
	// neither. A committed call here would be an orphan.
	if m, _ := db.GetMessage("call-row"); m != nil {
		t.Fatal("call row was committed despite the pair transaction failing — not atomic")
	}
}

// TestInsertToolCallPair_WritesBothRows: the happy path commits both the
// call and the result, sharing a turn, so CloseGroup sees a closed pair.
func TestInsertToolCallPair_WritesBothRows(t *testing.T) {
	db := testDB(t)
	if err := db.CreateThread(&threadv1.Thread{Id: "t1", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	call := &threadv1.Message{
		Id: "c", ThreadId: "t1", TurnId: "turn-1", Role: threadv1.Role_ROLE_ASSISTANT, Position: 0,
		Content: []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_ToolCall{
			ToolCall: &threadv1.ToolCallContent{Id: "x", Name: "Bash"},
		}}},
	}
	result := &threadv1.Message{
		Id: "r", ThreadId: "t1", TurnId: "turn-1", Role: threadv1.Role_ROLE_ASSISTANT, Position: 1,
		Content: []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_ToolResult{
			ToolResult: &threadv1.ToolResultContent{ToolCallId: "x", Content: "out"},
		}}},
	}
	if err := db.InsertToolCallPair(call, nil, result, nil); err != nil {
		t.Fatalf("InsertToolCallPair: %v", err)
	}
	gotCall, err := db.GetMessage("c")
	if err != nil {
		t.Fatalf("call not committed: %v", err)
	}
	gotResult, err := db.GetMessage("r")
	if err != nil {
		t.Fatalf("result not committed: %v", err)
	}
	if gotCall.TurnId != "turn-1" || gotResult.TurnId != "turn-1" {
		t.Fatalf("pair should share turn: call=%q result=%q", gotCall.TurnId, gotResult.TurnId)
	}
}

// TestMaxPosition covers the position-authority seed: -1 on an empty
// thread; the true high-water mark on a corpus carrying gaps and
// duplicated positions, where COUNT(*) understates it and a count-seeded
// counter would mint colliding positions.
func TestMaxPosition(t *testing.T) {
	db := testDB(t)
	if err := db.CreateThread(&threadv1.Thread{Id: "tp", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := db.MaxPosition("tp"); got != -1 {
		t.Fatalf("empty thread MaxPosition = %d, want -1", got)
	}
	for _, m := range []*threadv1.Message{
		{Id: "p0", ThreadId: "tp", Role: threadv1.Role_ROLE_USER, Position: 0},
		{Id: "p1", ThreadId: "tp", Role: threadv1.Role_ROLE_ASSISTANT, Position: 8},
		{Id: "p2", ThreadId: "tp", Role: threadv1.Role_ROLE_USER, Position: 8},
	} {
		if err := db.InsertMessage(m, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := db.MaxPosition("tp"); got != 8 {
		t.Fatalf("MaxPosition = %d, want 8 (COUNT(*) is 3)", got)
	}
}

// TestTurnStartPosition_SnapsToTurnBoundary verifies the Layer-2 branch
// snap: a position inside a turn resolves to that turn's first position,
// so a branch prefix never bisects a tool_call/tool_result pair. Turn
// starts and legacy empty-turn rows resolve to themselves.
func TestTurnStartPosition_SnapsToTurnBoundary(t *testing.T) {
	db := testDB(t)
	if err := db.CreateThread(&threadv1.Thread{Id: "t1", CreatedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	mk := func(id string, pos int64, turn string) *threadv1.Message {
		return &threadv1.Message{Id: id, ThreadId: "t1", TurnId: turn, Role: threadv1.Role_ROLE_ASSISTANT, Position: pos}
	}
	// Turn "a": positions 0,1. Turn "b" (a tool turn): positions 2,3,4.
	for _, m := range []*threadv1.Message{
		mk("m0", 0, "a"), mk("m1", 1, "a"),
		mk("m2", 2, "b"), mk("m3", 3, "b"), mk("m4", 4, "b"),
	} {
		if err := db.InsertMessage(m, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Any position inside turn "b" snaps to its start (2).
	for _, pos := range []int64{2, 3, 4} {
		got, err := db.TurnStartPosition("t1", pos)
		if err != nil {
			t.Fatal(err)
		}
		if got != 2 {
			t.Fatalf("TurnStartPosition(%d) = %d, want 2 (turn b start)", pos, got)
		}
	}
	// A turn start snaps to itself.
	if got, _ := db.TurnStartPosition("t1", 0); got != 0 {
		t.Fatalf("turn-start 0 should snap to 0, got %d", got)
	}
	// A legacy row with empty turn_id snaps to itself (no boundary to find).
	if err := db.InsertMessage(mk("m5", 5, ""), nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.TurnStartPosition("t1", 5); got != 5 {
		t.Fatalf("empty-turn row should snap to itself, got %d", got)
	}
	// A position with no row snaps to itself.
	if got, _ := db.TurnStartPosition("t1", 99); got != 99 {
		t.Fatalf("missing position should snap to itself, got %d", got)
	}
}
