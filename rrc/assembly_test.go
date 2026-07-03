package rrc

import (
	"context"
	"strings"
	"testing"

	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/proto/pbtext"
)

// hasTextBlock guards Local Context anchor reachback. The
// invariant: only externalized user / assistant text counts as a
// conversational anchor. Tool plumbing and internal thinking do not.
// A break here lets a deep tool loop hide the most-recent real reply.

func TestHasTextBlock_TextOnly(t *testing.T) {
	blocks := []*threadv1.ContentBlock{
		{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "hello"}}},
	}
	if !hasTextBlock(blocks) {
		t.Error("expected hasTextBlock=true for plain text content")
	}
}

func TestHasTextBlock_EmptyText(t *testing.T) {
	blocks := []*threadv1.ContentBlock{
		{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: ""}}},
	}
	if hasTextBlock(blocks) {
		t.Error("empty text block should not qualify as semantic content")
	}
}

func TestHasTextBlock_ToolOnlyMessage(t *testing.T) {
	toolCall := []*threadv1.ContentBlock{
		{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{
			Id: "t1", Name: "Read", Arguments: `{"path":"x"}`,
		}}},
	}
	if hasTextBlock(toolCall) {
		t.Error("tool_call-only content must not qualify as an anchor")
	}
	toolResult := []*threadv1.ContentBlock{
		{Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{
			ToolCallId: "t1", Content: "file contents here",
		}}},
	}
	if hasTextBlock(toolResult) {
		t.Error("tool_result-only content must not qualify as an anchor")
	}
}

func TestHasTextBlock_ThinkingOnly(t *testing.T) {
	blocks := []*threadv1.ContentBlock{
		{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "deliberating..."}}},
	}
	if hasTextBlock(blocks) {
		t.Error("thinking-only content must not qualify as a conversational anchor")
	}
}

func TestHasTextBlock_MixedPrefersText(t *testing.T) {
	blocks := []*threadv1.ContentBlock{
		{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "deliberating..."}}},
		{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "here's my reply"}}},
	}
	if !hasTextBlock(blocks) {
		t.Error("thinking+text content should qualify via the text block")
	}
}

func TestAssembleMissingExactCounterpartFails(t *testing.T) {
	scorer := newMockScorer()
	oracle := newMockChunkOracle()
	call := storedCall("call", "t1", "op", 0)
	anchor := makeMsg("q", 1, "t1", "current")
	oracle.Register(call.Id, pbtext.TextFromBlocks(call.Content))
	oracle.Register(anchor.Id, pbtext.TextFromBlocks(anchor.Content))
	scorer.SetScore(pbtext.TextFromBlocks(call.Content), strings.TrimSpace(pbtext.TextFromBlocks(anchor.Content)), 0.9)
	engine := testEngine(scorer, oracle)

	_, err := engine.Assemble(context.Background(), AssembleRequest{
		SerializedLocalContext: testSerializedLocalContext(anchor), Anchor: anchor,
		Corpus: []*threadv1.Message{call, anchor}, LocalContext: []*threadv1.Message{anchor},
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
	}
	scorer.SetScore(pbtext.TextFromBlocks(call.Content), strings.TrimSpace(pbtext.TextFromBlocks(anchor.Content)), 0.9)
	scorer.SetScore(pbtext.TextFromBlocks(result.Content), strings.TrimSpace(pbtext.TextFromBlocks(anchor.Content)), 0.8)
	engine := testEngine(scorer, oracle)
	localTokens := engine.Config().Chunk.Estimate(pbtext.TextFromBlocks(anchor.Content))

	assembled, err := engine.Assemble(context.Background(), AssembleRequest{
		SerializedLocalContext: testSerializedLocalContext(anchor), Anchor: anchor,
		Corpus: []*threadv1.Message{call, result, anchor}, LocalContext: []*threadv1.Message{anchor},
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
