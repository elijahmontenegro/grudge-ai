package rrc

import (
	"testing"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// fakeResolver — Resolver stub for rule tests. Given a list of
// candidates in score-order, it hands them out sequentially,
// tracking used-set + honoring ExcludeIDs seed. Filter applied
// per-candidate so tests can mix candidate types.
type fakeResolver struct {
	candidates []*pb.Message
	scores     map[string]float64
	used       map[string]bool
	picks      []ResolverPick
}

func newFakeResolver(candidates ...*pb.Message) *fakeResolver {
	return &fakeResolver{
		candidates: candidates,
		scores:     make(map[string]float64),
		used:       make(map[string]bool),
	}
}

// SetScore assigns a score used by BestCandidate's pick log.
// Candidates without a score default to 0.
func (r *fakeResolver) SetScore(id string, score float64) {
	r.scores[id] = score
}

func (r *fakeResolver) ExcludeIDs(ids []string) {
	for _, id := range ids {
		r.used[id] = true
	}
}

func (r *fakeResolver) BestCandidate(filter func(*pb.Message) bool) (*pb.Message, error) {
	for _, m := range r.candidates {
		if r.used[m.Id] {
			continue
		}
		if filter(m) {
			r.used[m.Id] = true
			r.picks = append(r.picks, ResolverPick{MsgID: m.Id, Score: r.scores[m.Id]})
			return m, nil
		}
	}
	return nil, nil
}

func (r *fakeResolver) Picks() []ResolverPick { return r.picks }

// === Fixture builders ===

func sysMsg(text string) *pb.LLMMessage {
	return &pb.LLMMessage{
		Role:    pb.Role_ROLE_SYSTEM,
		Content: []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: text}}}},
	}
}

func userMsg(text string) *pb.LLMMessage {
	return &pb.LLMMessage{
		Role:    pb.Role_ROLE_USER,
		Content: []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: text}}}},
	}
}

func asstToolCall(id, name, args string) *pb.LLMMessage {
	return &pb.LLMMessage{
		Role: pb.Role_ROLE_ASSISTANT,
		Content: []*pb.ContentBlock{{
			Block: &pb.ContentBlock_ToolCall{ToolCall: &pb.ToolCallContent{Id: id, Name: name, Arguments: args}},
		}},
	}
}

func toolResp(callID, content string) *pb.LLMMessage {
	return &pb.LLMMessage{
		Role: pb.Role_ROLE_ASSISTANT,
		Content: []*pb.ContentBlock{{
			Block: &pb.ContentBlock_ToolResult{ToolResult: &pb.ToolResultContent{ToolCallId: callID, Content: content}},
		}},
	}
}

func storedUser(id, text string) *pb.Message {
	return &pb.Message{
		Id: id, Role: pb.Role_ROLE_USER,
		Content: []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: text}}}},
	}
}

func storedCall(id, callID, name, args string) *pb.Message {
	return &pb.Message{
		Id: id, Role: pb.Role_ROLE_ASSISTANT,
		Content: []*pb.ContentBlock{{
			Block: &pb.ContentBlock_ToolCall{ToolCall: &pb.ToolCallContent{Id: callID, Name: name, Arguments: args}},
		}},
	}
}

func storedResult(id, callID, content string) *pb.Message {
	return &pb.Message{
		Id: id, Role: pb.Role_ROLE_ASSISTANT,
		Content: []*pb.ContentBlock{{
			Block: &pb.ContentBlock_ToolResult{ToolResult: &pb.ToolResultContent{ToolCallId: callID, Content: content}},
		}},
	}
}

// === EnsureUserAnchor ===

func TestEnsureUserAnchor_NoopWhenUserPresent(t *testing.T) {
	wire := []*pb.LLMMessage{sysMsg("s"), userMsg("hi"), asstToolCall("c1", "Read", "{}")}
	r := newFakeResolver(storedUser("m1", "from-store"))
	out, err := EnsureUserAnchor(wire, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(wire) {
		t.Fatalf("should be no-op; got len=%d want=%d", len(out), len(wire))
	}
}

func TestEnsureUserAnchor_InsertsAtPositionOne(t *testing.T) {
	wire := []*pb.LLMMessage{sysMsg("s"), asstToolCall("c1", "Read", "{}"), toolResp("c1", "...")}
	r := newFakeResolver(storedUser("m1", "anchor-text"))
	out, err := EnsureUserAnchor(wire, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("expected wire grown by 1; got len=%d", len(out))
	}
	if out[0].Role != pb.Role_ROLE_SYSTEM {
		t.Errorf("system should still be at [0]")
	}
	if out[1].Role != pb.Role_ROLE_USER {
		t.Errorf("user anchor should be at [1]; got role=%v", out[1].Role)
	}
	if got := out[1].Content[0].GetText().Text; got != "anchor-text" {
		t.Errorf("anchor text = %q; want %q", got, "anchor-text")
	}
}

func TestEnsureUserAnchor_NoCandidateAvailable(t *testing.T) {
	wire := []*pb.LLMMessage{sysMsg("s"), asstToolCall("c1", "Read", "{}"), toolResp("c1", "...")}
	r := newFakeResolver() // empty store
	out, err := EnsureUserAnchor(wire, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(wire) {
		t.Fatalf("no candidate → wire unchanged; got len=%d", len(out))
	}
}

func TestEnsureUserAnchor_ExtractsOnlyText(t *testing.T) {
	// Candidate is a user message with text — result should be user+text, only one block.
	wire := []*pb.LLMMessage{sysMsg("s"), asstToolCall("c1", "Read", "{}")}
	r := newFakeResolver(storedUser("m1", "the-ask"))
	out, _ := EnsureUserAnchor(wire, r)
	if len(out[1].Content) != 1 {
		t.Errorf("anchor should have 1 block; got %d", len(out[1].Content))
	}
}

// === PairToolCallsWithResults ===

func TestPairToolCallsWithResults_NoopWhenPaired(t *testing.T) {
	wire := []*pb.LLMMessage{asstToolCall("c1", "Read", "{}"), toolResp("c1", "body")}
	r := newFakeResolver(storedResult("m1", "xx", "other-body"))
	out, _ := PairToolCallsWithResults(wire, r)
	if len(out) != 2 {
		t.Fatalf("paired wire should be unchanged; got len=%d", len(out))
	}
}

func TestPairToolCallsWithResults_InsertsResultAfterOrphanCall(t *testing.T) {
	wire := []*pb.LLMMessage{asstToolCall("c1", "Read", `{"path":"/a"}`)}
	r := newFakeResolver(storedResult("m1", "xyz", "candidate-body"))
	out, _ := PairToolCallsWithResults(wire, r)
	if len(out) != 2 {
		t.Fatalf("expected wire grown by 1; got len=%d", len(out))
	}
	// [0] is the original call
	tc := out[0].Content[0].GetToolCall()
	if tc == nil || tc.Id != "c1" {
		t.Errorf("[0] should be original call c1; got %+v", out[0])
	}
	// [1] is the extracted result with tool_call_id rebound to c1
	tr := out[1].Content[0].GetToolResult()
	if tr == nil || tr.ToolCallId != "c1" {
		t.Errorf("[1] tool_call_id should be rebound to c1; got %+v", out[1])
	}
	if tr.Content != "candidate-body" {
		t.Errorf("[1] content should be candidate body; got %q", tr.Content)
	}
}

func TestPairToolCallsWithResults_UsesDistinctCandidatesForMultipleOrphans(t *testing.T) {
	// Two orphan calls. Two candidates available. Each orphan should get a different one.
	wire := []*pb.LLMMessage{asstToolCall("c1", "A", "{}"), asstToolCall("c2", "B", "{}")}
	r := newFakeResolver(
		storedResult("m1", "aaa", "body-1"),
		storedResult("m2", "bbb", "body-2"),
	)
	out, _ := PairToolCallsWithResults(wire, r)
	if len(out) != 4 {
		t.Fatalf("expected 4 messages; got %d", len(out))
	}
	// Shape: call c1, result (for c1), call c2, result (for c2)
	tr1 := out[1].Content[0].GetToolResult()
	tr2 := out[3].Content[0].GetToolResult()
	if tr1.ToolCallId != "c1" || tr2.ToolCallId != "c2" {
		t.Errorf("ids should bind: got %q %q", tr1.ToolCallId, tr2.ToolCallId)
	}
	if tr1.Content == tr2.Content {
		t.Errorf("distinct candidates expected; got duplicate body %q", tr1.Content)
	}
}

func TestPairToolCallsWithResults_DropsWhenNoCandidate(t *testing.T) {
	wire := []*pb.LLMMessage{asstToolCall("c1", "A", "{}")}
	r := newFakeResolver() // empty
	out, _ := PairToolCallsWithResults(wire, r)
	if len(out) != 1 {
		t.Fatalf("no candidate → wire unchanged; got len=%d", len(out))
	}
}

// === PairToolResultsWithCalls ===

func TestPairToolResultsWithCalls_InsertsCallBeforeOrphanResult(t *testing.T) {
	wire := []*pb.LLMMessage{toolResp("xyz", "result-body")}
	r := newFakeResolver(storedCall("m1", "other-id", "Read", `{"path":"/a"}`))
	out, _ := PairToolResultsWithCalls(wire, r)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages; got %d", len(out))
	}
	// [0] is the extracted call with id rebound to xyz
	tc := out[0].Content[0].GetToolCall()
	if tc == nil || tc.Id != "xyz" {
		t.Errorf("extracted call id should be rebound to xyz; got %+v", tc)
	}
	if tc.Name != "Read" {
		t.Errorf("fn name should be Read; got %q", tc.Name)
	}
	// [1] is the original orphan
	tr := out[1].Content[0].GetToolResult()
	if tr == nil || tr.ToolCallId != "xyz" {
		t.Errorf("original orphan should still be at [1]; got %+v", out[1])
	}
}

// === Resolver behavior ===

func TestResolver_UsedSetPreventsReuse(t *testing.T) {
	cand := storedResult("m1", "cid", "body")
	r := newFakeResolver(cand)
	got1, _ := r.BestCandidate(hasToolResult)
	got2, _ := r.BestCandidate(hasToolResult)
	if got1 == nil || got2 != nil {
		t.Errorf("first pick returns candidate, second should return nil; got1=%v got2=%v", got1 != nil, got2 != nil)
	}
}

func TestResolver_ExcludeIDsSeedsUsed(t *testing.T) {
	cand := storedUser("m1", "x")
	r := newFakeResolver(cand)
	r.ExcludeIDs([]string{"m1"})
	got, _ := r.BestCandidate(isUserAnchor)
	if got != nil {
		t.Errorf("excluded id m1 should not be picked; got %+v", got)
	}
}

// === Pipeline composition ===

func TestApply_PipelineOrder(t *testing.T) {
	// Wire: system + orphan tool_result. Pipeline should pair it (insert call)
	// and also ensure user anchor.
	wire := []*pb.LLMMessage{sysMsg("s"), toolResp("xyz", "body")}
	r := newFakeResolver(
		storedCall("m1", "cid", "Read", "{}"),
		storedUser("m2", "the-ask"),
	)
	r.SetScore("m1", 0.7)
	r.SetScore("m2", 0.9)
	out, scores, err := Apply(wire, r, PairToolResultsWithCalls, PairToolCallsWithResults, EnsureUserAnchor)
	if err != nil {
		t.Fatal(err)
	}
	// Expected: system, user-anchor, call, result.
	if len(out) != 4 {
		t.Fatalf("expected 4 messages; got %d", len(out))
	}
	if out[0].Role != pb.Role_ROLE_SYSTEM {
		t.Errorf("[0] expected system; got role=%v", out[0].Role)
	}
	if out[1].Role != pb.Role_ROLE_USER {
		t.Errorf("[1] expected user anchor; got role=%v", out[1].Role)
	}
	if tc := out[2].Content[0].GetToolCall(); tc == nil || tc.Id != "xyz" {
		t.Errorf("[2] expected call with id=xyz; got %+v", out[2])
	}
	if tr := out[3].Content[0].GetToolResult(); tr == nil || tr.ToolCallId != "xyz" {
		t.Errorf("[3] expected result for xyz; got %+v", out[3])
	}
	// Score map should carry scores for the two rectification inserts.
	if len(scores) != 2 {
		t.Errorf("expected 2 insert scores; got %d", len(scores))
	}
	if got := scores[out[1]]; got != 0.9 {
		t.Errorf("anchor insert score = %v; want 0.9", got)
	}
	if got := scores[out[2]]; got != 0.7 {
		t.Errorf("pair-call insert score = %v; want 0.7", got)
	}
}
