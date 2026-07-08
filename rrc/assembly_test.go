package rrc

import (
	"context"
	"strings"
	"testing"

	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/proto/pbtext"
)

// hasSemanticBlock decides what counts as semantic Local Context content.
// The invariant: user/assistant text and thinking are co-equal semantic
// anchors — thinking is the model's own reasoning, the primary
// disambiguation signal, and no path may privilege one semantic block type
// over another. Tool plumbing, images, and attachments are turn record,
// never discourse.

func TestHasSemanticBlock_TextOnly(t *testing.T) {
	blocks := []*threadv1.ContentBlock{
		{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "hello"}}},
	}
	if !hasSemanticBlock(blocks) {
		t.Error("expected hasSemanticBlock=true for plain text content")
	}
}

func TestHasSemanticBlock_EmptyBlocks(t *testing.T) {
	blocks := []*threadv1.ContentBlock{
		{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: ""}}},
		{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: ""}}},
	}
	if hasSemanticBlock(blocks) {
		t.Error("empty text/thinking blocks should not qualify as semantic content")
	}
}

func TestHasSemanticBlock_ToolOnlyMessage(t *testing.T) {
	toolCall := []*threadv1.ContentBlock{
		{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{
			Id: "t1", Name: "Read", Arguments: `{"path":"x"}`,
		}}},
	}
	if hasSemanticBlock(toolCall) {
		t.Error("tool_call-only content must not qualify as semantic")
	}
	toolResult := []*threadv1.ContentBlock{
		{Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{
			ToolCallId: "t1", Content: "file contents here",
		}}},
	}
	if hasSemanticBlock(toolResult) {
		t.Error("tool_result-only content must not qualify as semantic")
	}
}

func TestHasSemanticBlock_ThinkingOnly(t *testing.T) {
	// A thinking-only assistant step IS a full semantic anchor — the
	// architect's ruling: thinking is the biggest source of disambiguation
	// and is never ignored. (This inverts the pre-remediation behavior,
	// which was blind to thinking and privileged text.)
	blocks := []*threadv1.ContentBlock{
		{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "deliberating..."}}},
	}
	if !hasSemanticBlock(blocks) {
		t.Error("thinking-only content must qualify as a semantic anchor")
	}
}

// TestAssemble_TurnDeliverySplit pins the three-role split: Local Context
// is the semantic discourse (query + anchor, text and thinking only), the
// TurnDelivery record carries the whole in-flight turn — tools included —
// to the wire through real protocol closure, and the serialized MEMBERSHIP
// covers the delivered union so a delivered tool message is never
// re-retrievable as a candidate. The wire may legitimately end on a
// tool_result while the anchor is the last SEMANTIC message: those are two
// different roles' "last elements".
func TestAssemble_TurnDeliverySplit(t *testing.T) {
	mc := newMockScorer()
	o := newMockChunkOracle()
	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}
	cfg.DiversityLambda = 0
	cfg.MinBatchStdDev = 0
	e := NewEngine(cfg, mc, WithChunkOracle(o))

	trigger := addMsg(o, "trigger", 0, "t1", "what was the password?")
	thinking := &threadv1.Message{
		Id: "think", ThreadId: "t1", Position: 1, Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_Thinking{
			Thinking: &threadv1.ThinkingContent{Text: "the user wants the stored passphrase"},
		}}},
	}
	call := storedCall("tcall", "t1", "op-9", 2)
	result := storedResult("tresult", "t1", "op-9", 3)
	// The delivered tool result is also indexed as a retrieval candidate —
	// the union membership must exclude it.
	o.Register("tresult", "tool result body")
	o.threads["tresult"] = "t1"

	turnRecord := []*threadv1.Message{trigger, thinking, call, result}
	semantic := SemanticMessages(turnRecord)
	if len(semantic) != 2 || semantic[0].Id != "trigger" || semantic[1].Id != "think" {
		t.Fatalf("semantic projection = %v, want [trigger think]", messageIDs(semantic))
	}

	res, err := e.Assemble(context.Background(), AssembleRequest{
		Anchor:       semantic[len(semantic)-1], // last SEMANTIC message: the thinking
		Store:        sliceStore(turnRecord),
		LocalContext: semantic,
		TurnDelivery: turnRecord,
		Scope:        threadv1.SelectionScope_SELECTION_SCOPE_THREAD,
		ThreadID:     "t1",
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// The whole turn reached the wire in position order — closure held over
	// the union — and the wire's last element is the tool result even
	// though the anchor is the thinking.
	if got := len(res.Wire); got != 4 {
		t.Fatalf("wire len=%d, want 4 (trigger, thinking, call, result)", got)
	}
	last := res.Wire[len(res.Wire)-1]
	if tr := last.Content[len(last.Content)-1].GetToolResult(); tr == nil || tr.ToolCallId != "op-9" {
		t.Fatalf("wire must end on the turn's tool_result (delivery order), got %+v", last)
	}

	// Membership = the union (tools included): nothing delivered is
	// re-retrievable. The query chunks stay semantic-only.
	sl := res.SerializedLocalContext
	if sl == nil {
		t.Fatal("expected a serialized local context")
	}
	if !sameIDs(sl.MessageIDs, []string{"trigger", "think", "tcall", "tresult"}) {
		t.Fatalf("membership must cover the delivered union, got %v", sl.MessageIDs)
	}
	for _, c := range sl.Chunks {
		if contains(c.Text, "[tool_call") || contains(c.Text, "[tool_result") {
			t.Fatalf("tool block leaked into the query chunks:\n%s", c.Text)
		}
	}
	if res.Selection != nil {
		for _, s := range res.Selection.Selected {
			if s.MessageId == "tresult" || s.MessageId == "tcall" {
				t.Fatalf("delivered tool message re-retrieved as a candidate: %v", s.MessageId)
			}
		}
	}
}

func TestAssembleMissingExactCounterpartFails(t *testing.T) {
	scorer := newMockScorer()
	oracle := newMockChunkOracle()
	call := storedCall("call", "t1", "op", 0)
	anchor := makeMsg("q", 1, "t1", "current")
	oracle.Register(call.Id, pbtext.TextFromBlocks(call.Content))
	oracle.threads[call.Id] = call.ThreadId
	oracle.Register(anchor.Id, pbtext.TextFromBlocks(anchor.Content))
	oracle.threads[anchor.Id] = anchor.ThreadId
	scorer.SetScore(pbtext.TextFromBlocks(call.Content), strings.TrimSpace(pbtext.TextFromBlocks(anchor.Content)), 0.9)
	engine := testEngine(scorer, oracle)

	_, err := engine.Assemble(context.Background(), AssembleRequest{
		SerializedLocalContext: testSerializedLocalContext(anchor), Anchor: anchor,
		Store: sliceStore([]*threadv1.Message{call, anchor}), LocalContext: []*threadv1.Message{anchor},
		Scope: threadv1.SelectionScope_SELECTION_SCOPE_THREAD, ThreadID: "t1",
	})
	if err == nil || !strings.Contains(err.Error(), "requires exact tool result") {
		t.Fatalf("expected integrity error, got %v", err)
	}
}

func TestAssembleShedsSelectedProtocolClosureAtomically(t *testing.T) {
	scorer := newMockScorer()
	oracle := newMockChunkOracle()
	call := storedCall("call", "t1", "op", 0)
	result := storedResult("result", "t1", "op", 1)
	anchor := makeMsg("q", 2, "t1", "current")
	for _, message := range []*threadv1.Message{call, result, anchor} {
		oracle.Register(message.Id, pbtext.TextFromBlocks(message.Content))
		oracle.threads[message.Id] = message.ThreadId
	}
	scorer.SetScore(pbtext.TextFromBlocks(call.Content), strings.TrimSpace(pbtext.TextFromBlocks(anchor.Content)), 0.9)
	scorer.SetScore(pbtext.TextFromBlocks(result.Content), strings.TrimSpace(pbtext.TextFromBlocks(anchor.Content)), 0.8)
	engine := testEngine(scorer, oracle)
	localTokens := engine.Config().Chunk.Estimate(pbtext.TextFromBlocks(anchor.Content))

	assembled, err := engine.Assemble(context.Background(), AssembleRequest{
		SerializedLocalContext: testSerializedLocalContext(anchor), Anchor: anchor,
		Store: sliceStore([]*threadv1.Message{call, result, anchor}), LocalContext: []*threadv1.Message{anchor},
		Scope: threadv1.SelectionScope_SELECTION_SCOPE_THREAD, ThreadID: "t1",
		Budget: localTokens + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range assembled.Wire {
		for _, block := range message.Content {
			if block.GetToolCall() != nil || block.GetToolResult() != nil {
				t.Fatal("budgeting emitted a partial selected protocol group")
			}
		}
	}
	if len(assembled.Shed) == 0 {
		t.Fatal("expected selected protocol group to be shed")
	}
}

func TestDeliveryGroupsPreserveStoredChronology(t *testing.T) {
	call := storedCall("call", "t1", "op", 0)
	middle := makeMsg("middle", 1, "t1", "between call and result")
	result := storedResult("result", "t1", "op", 2)
	index := NewProtocolIndex([]*threadv1.Message{call, middle, result})

	callGroup, err := index.CloseGroup(call, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	middleGroup, err := index.CloseGroup(middle, 0.8)
	if err != nil {
		t.Fatal(err)
	}
	wire := groupsToWire([]DeliveryGroup{callGroup, middleGroup}, nil)
	if len(wire) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(wire))
	}
	if got := strings.TrimSpace(pbtext.TextFromBlocks(wire[1].Content)); got != "between call and result" {
		t.Fatalf("middle message moved out of chronology: %q", got)
	}
}

func TestEquivalentProtocolClosuresShedTogether(t *testing.T) {
	call := storedCall("call", "t1", "op", 0)
	result := storedResult("result", "t1", "op", 1)
	index := NewProtocolIndex([]*threadv1.Message{call, result})
	callGroup, err := index.CloseGroup(call, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	resultGroup, err := index.CloseGroup(result, 0.8)
	if err != nil {
		t.Fatal(err)
	}
	groups := mergeDeliveryGroups([]DeliveryGroup{callGroup, resultGroup})
	if len(groups) != 1 {
		t.Fatalf("expected one closure group, got %d", len(groups))
	}
	if strings.Join(groups[0].RootIDs, ",") != "call,result" {
		t.Fatalf("unexpected roots: %v", groups[0].RootIDs)
	}
}
