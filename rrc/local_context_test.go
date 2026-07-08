package rrc

import (
	"fmt"
	"slices"
	"testing"

	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

func localMessage(id string, role threadv1.Role, position int64, blocks ...*threadv1.ContentBlock) *threadv1.Message {
	return &threadv1.Message{Id: id, ThreadId: "t1", Role: role, Position: position, Content: blocks}
}

func textBlock(text string) *threadv1.ContentBlock {
	return &threadv1.ContentBlock{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: text}}}
}

func TestBuildLocalContextBoundsAndReachesBackForAnchors(t *testing.T) {
	corpus := []*threadv1.Message{
		localMessage("user", threadv1.Role_ROLE_USER, 0, textBlock("original ask")),
		localMessage("assistant", threadv1.Role_ROLE_ASSISTANT, 1, textBlock("working on it")),
		storedCall("call", "t1", "op", 2),
		storedResult("result", "t1", "op", 3),
	}
	local := BuildLocalContext(corpus, 2)
	got := messageIDs(local)
	want := []string{"user", "assistant", "call", "result"}
	if !sameIDs(got, want) {
		t.Fatalf("Local Context ids=%v, want %v", got, want)
	}
}

func TestSerializedLocalContextIsSemanticOnlyAndKeepsFullMembership(t *testing.T) {
	// The QUERY (Chunks) is semantic-only — user/assistant text and thinking,
	// co-equal; tool blocks never enter it (they are turn record, not
	// discourse). MEMBERSHIP (MessageIDs) still carries every message,
	// tools included, so nothing delivered is ever re-retrieved.
	thinking := &threadv1.ContentBlock{Block: &threadv1.ContentBlock_Thinking{
		Thinking: &threadv1.ThinkingContent{Text: "the user wants the file inspected"},
	}}
	local := []*threadv1.Message{
		localMessage("u", threadv1.Role_ROLE_USER, 0, textBlock("inspect the file")),
		localMessage("th", threadv1.Role_ROLE_ASSISTANT, 1, thinking),
		storedCall("c", "t1", "op-1", 2),
		storedResult("r", "t1", "op-1", 3),
	}
	serialized := SerializeLocalContext(local, testChunkConfig())
	if serialized == nil {
		t.Fatal("serialization is nil")
	}
	if !sameIDs(serialized.MessageIDs, []string{"u", "th", "c", "r"}) {
		t.Fatalf("membership must include every message (tools too), got %v", serialized.MessageIDs)
	}
	joined := ""
	for _, part := range serialized.Chunks {
		joined += part.Text
	}
	for _, label := range []string{"role=user", "inspect the file", "[thinking]", "the user wants the file inspected"} {
		if !contains(joined, label) {
			t.Fatalf("semantic serialization missing %q:\n%s", label, joined)
		}
	}
	for _, label := range []string{"[tool_call", "[tool_result"} {
		if contains(joined, label) {
			t.Fatalf("tool block leaked into the query serialization (%q):\n%s", label, joined)
		}
	}
}

func TestSerializeLocalContext_PerMessageChunks(t *testing.T) {
	// Q is a span of independent items: each semantic message is its own
	// query unit and a chunk NEVER spans two messages — fusing a recall
	// question with an unrelated neighbor is the measured dilution this
	// guards against. Indices are global and monotonic (the score cache
	// keys on (fingerprint, chunk index); a per-message restart would
	// collide entries across messages).
	local := []*threadv1.Message{
		localMessage("u", threadv1.Role_ROLE_USER, 0, textBlock("alpha question about the archive passphrase")),
		localMessage("a", threadv1.Role_ROLE_ASSISTANT, 1, textBlock("beta answer about boiling water")),
		storedCall("c", "t1", "op-1", 2), // tool-only: membership, no chunk
	}
	serialized := SerializeLocalContext(local, testChunkConfig())
	if serialized == nil {
		t.Fatal("serialization is nil")
	}
	if !sameIDs(serialized.MessageIDs, []string{"u", "a", "c"}) {
		t.Fatalf("membership ids=%v", serialized.MessageIDs)
	}
	if len(serialized.Chunks) != 2 {
		t.Fatalf("want one chunk per semantic message (2), got %d: %+v", len(serialized.Chunks), serialized.Chunks)
	}
	for i, c := range serialized.Chunks {
		if c.Index != i {
			t.Fatalf("chunk indices must be global and monotonic: chunk %d has Index %d", i, c.Index)
		}
		hasAlpha := contains(c.Text, "alpha")
		hasBeta := contains(c.Text, "beta")
		if hasAlpha && hasBeta {
			t.Fatalf("chunk spans two messages (fusion — the dilution bug):\n%s", c.Text)
		}
		if !hasAlpha && !hasBeta {
			t.Fatalf("chunk carries neither message's content:\n%s", c.Text)
		}
	}
}

func TestSerializeLocalContext_LongMessageSplitsWithGlobalIndices(t *testing.T) {
	// A single oversized message still splits into multiple chunks — all
	// from that message alone — and indices keep advancing globally across
	// the following message.
	long := ""
	for range 40 {
		long += "the archive passphrase discussion continues with more detail. "
	}
	cfg := testChunkConfig()
	cfg.MaxChars = 400
	local := []*threadv1.Message{
		localMessage("big", threadv1.Role_ROLE_USER, 0, textBlock(long)),
		localMessage("next", threadv1.Role_ROLE_ASSISTANT, 1, textBlock("short reply")),
	}
	serialized := SerializeLocalContext(local, cfg)
	if serialized == nil {
		t.Fatal("serialization is nil")
	}
	if len(serialized.Chunks) < 3 {
		t.Fatalf("oversized message should split (plus the short reply), got %d chunks", len(serialized.Chunks))
	}
	for i, c := range serialized.Chunks {
		if c.Index != i {
			t.Fatalf("global index broken at chunk %d: Index=%d", i, c.Index)
		}
	}
	last := serialized.Chunks[len(serialized.Chunks)-1]
	if !contains(last.Text, "short reply") {
		t.Fatalf("final chunk should be the second message's own unit:\n%s", last.Text)
	}
	if contains(last.Text, "passphrase discussion") {
		t.Fatalf("second message's chunk absorbed the first message's text:\n%s", last.Text)
	}
}

func TestSerializedLocalContextFingerprintChangesWithOrderRoleAndContent(t *testing.T) {
	a := localMessage("a", threadv1.Role_ROLE_USER, 0, textBlock("alpha"))
	b := localMessage("b", threadv1.Role_ROLE_ASSISTANT, 1, textBlock("beta"))
	base := SerializeLocalContext([]*threadv1.Message{a, b}, testChunkConfig()).Fingerprint
	reordered := SerializeLocalContext([]*threadv1.Message{b, a}, testChunkConfig()).Fingerprint
	roleChanged := SerializeLocalContext([]*threadv1.Message{
		localMessage("a", threadv1.Role_ROLE_ASSISTANT, 0, textBlock("alpha")), b,
	}, testChunkConfig()).Fingerprint
	contentChanged := SerializeLocalContext([]*threadv1.Message{
		localMessage("a", threadv1.Role_ROLE_USER, 0, textBlock("changed")), b,
	}, testChunkConfig()).Fingerprint
	if base == reordered || base == roleChanged || base == contentChanged {
		t.Fatal("fingerprint must bind order, role, and exact serialized content")
	}
}

func TestSelectPrerequisitesCacheUsesFingerprint(t *testing.T) {
	scorer := newMockScorer()
	scorer.SetScore("prior", "query", 0.9)
	oracle := newMockChunkOracle()
	addMsg(oracle, "p", 0, "t1", "prior") // registered as a candidate via the oracle
	anchor := addMsg(oracle, "q", 1, "t1", "query")
	engine := testEngine(scorer, oracle)
	serialized := testSerializedLocalContext(anchor)

	if _, _, err := engine.SelectPrerequisites(t.Context(), serialized, anchor, threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.SelectPrerequisites(t.Context(), serialized, anchor, threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1"); err != nil {
		t.Fatal(err)
	}
	if scorer.callCount != 1 {
		t.Fatalf("exact serialized Local Context should hit cache; scorer calls=%d", scorer.callCount)
	}
	changed := *serialized
	changed.Fingerprint = serialized.Fingerprint + "-changed"
	if _, _, err := engine.SelectPrerequisites(t.Context(), &changed, anchor, threadv1.SelectionScope_SELECTION_SCOPE_THREAD, "t1"); err != nil {
		t.Fatal(err)
	}
	if scorer.callCount != 2 {
		t.Fatalf("changed fingerprint should miss cache; scorer calls=%d", scorer.callCount)
	}
}

func withTurn(m *threadv1.Message, turnID string) *threadv1.Message {
	m.TurnId = turnID
	return m
}

// TestBuildActiveDiscourse_TurnScopedWindow: Local Context is exactly the
// active turn's messages, in corpus order, regardless of turn length. A
// long tool loop that a fixed last-N window would truncate is kept whole.
func TestBuildActiveDiscourse_TurnScopedWindow(t *testing.T) {
	// Prior completed turn (turn-A) + a long current turn (turn-B) whose
	// tool loop is longer than any small fixed N.
	corpus := []*threadv1.Message{
		withTurn(localMessage("u0", threadv1.Role_ROLE_USER, 0, textBlock("earlier ask")), "turn-A"),
		withTurn(localMessage("a0", threadv1.Role_ROLE_ASSISTANT, 1, textBlock("earlier answer")), "turn-A"),
		withTurn(localMessage("u1", threadv1.Role_ROLE_USER, 2, textBlock("current ask")), "turn-B"),
	}
	// 20-step tool loop in the current turn.
	pos := int64(3)
	for i := range 20 {
		corpus = append(corpus, withTurn(storedCall(fmt.Sprintf("c%d", i), "t1", "op", pos), "turn-B"))
		pos++
		corpus = append(corpus, withTurn(storedResult(fmt.Sprintf("r%d", i), "t1", "op", pos), "turn-B"))
		pos++
	}

	local := BuildActiveDiscourse(corpus, "turn-B", 2)
	got := messageIDs(local)

	// The current turn has a user-text anchor (u1) but no assistant-text
	// anchor of its own (tool loop only), so reach-back pulls the prior
	// assistant anchor a0. Window = u1 + 40 tool msgs (41); +a0 = 42.
	// A fixed last-N=2 would have kept only the final result pair.
	if len(got) != 42 {
		t.Fatalf("active discourse len=%d, want 42 (a0 anchor + u1 + 20 call/result pairs)", len(got))
	}
	for _, id := range []string{"u1", "c0", "r0", "c19", "r19"} {
		if !containsID(got, id) {
			t.Fatalf("active discourse missing %q (turn truncated?): %v", id, got)
		}
	}
	// u0 (prior turn's user, already satisfied by u1) must NOT be pulled.
	if containsID(got, "u0") {
		t.Fatalf("prior turn's redundant user anchor leaked into active discourse: %v", got)
	}
	// a0 IS present — reached back as the missing assistant-text anchor.
	if !containsID(got, "a0") {
		t.Fatalf("assistant anchor a0 should be reached back: %v", got)
	}
}

// TestBuildActiveDiscourse_ReachesBackForAnchors: when the current turn
// lacks an assistant-text anchor, the builder reaches back for it (shared
// reach-back with BuildLocalContext) so the span disambiguates.
func TestBuildActiveDiscourse_ReachesBackForAnchors(t *testing.T) {
	corpus := []*threadv1.Message{
		withTurn(localMessage("u0", threadv1.Role_ROLE_USER, 0, textBlock("original ask")), "turn-A"),
		withTurn(localMessage("a0", threadv1.Role_ROLE_ASSISTANT, 1, textBlock("prior assistant text")), "turn-A"),
		// Current turn is a tool-only continuation: no assistant text of its own.
		withTurn(storedCall("c", "t1", "op", 2), "turn-B"),
		withTurn(storedResult("r", "t1", "op", 3), "turn-B"),
	}
	local := BuildActiveDiscourse(corpus, "turn-B", 2)
	got := messageIDs(local)
	// Reach-back pulls the missing assistant anchor (a0) — and its
	// preceding user anchor is already satisfied by... none in-window, so
	// u0 is also reached. Window itself is c,r.
	if !containsID(got, "a0") {
		t.Fatalf("reach-back did not pull assistant anchor: %v", got)
	}
	if !containsID(got, "c") || !containsID(got, "r") {
		t.Fatalf("active turn window missing: %v", got)
	}
}

// TestBuildActiveDiscourse_FallsBackWhenNoTurnID: empty turn id (legacy
// rows / autonomous first call) falls back to the bounded recency window,
// preserving prior behavior.
func TestBuildActiveDiscourse_FallsBackWhenNoTurnID(t *testing.T) {
	corpus := []*threadv1.Message{
		localMessage("user", threadv1.Role_ROLE_USER, 0, textBlock("original ask")),
		localMessage("assistant", threadv1.Role_ROLE_ASSISTANT, 1, textBlock("working on it")),
		storedCall("call", "t1", "op", 2),
		storedResult("result", "t1", "op", 3),
	}
	// Empty turn id → identical to BuildLocalContext(corpus, 2).
	got := messageIDs(BuildActiveDiscourse(corpus, "", 2))
	want := messageIDs(BuildLocalContext(corpus, 2))
	if !sameIDs(got, want) {
		t.Fatalf("fallback mismatch: active=%v localContext=%v", got, want)
	}
}

// TestBuildActiveDiscourse_UnknownTurnIDFallsBack: a turn id present on the
// RRCLLM but with no stored message yet (autonomous tick before any event
// of the tick lands) falls back to recency rather than returning empty.
func TestBuildActiveDiscourse_UnknownTurnIDFallsBack(t *testing.T) {
	corpus := []*threadv1.Message{
		localMessage("user", threadv1.Role_ROLE_USER, 0, textBlock("ask")),
		localMessage("assistant", threadv1.Role_ROLE_ASSISTANT, 1, textBlock("answer")),
	}
	got := messageIDs(BuildActiveDiscourse(corpus, "turn-not-yet-stored", 2))
	if len(got) == 0 {
		t.Fatal("unknown turn id must fall back to recency, not return empty")
	}
}

func containsID(ids []string, id string) bool {
	return slices.Contains(ids, id)
}

func contains(text, part string) bool {
	for i := 0; i+len(part) <= len(text); i++ {
		if text[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
