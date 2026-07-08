package agent

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync/atomic"
	"testing"

	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/service/storage"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// runnerTestEstimator: the estimator lives on chunk.Config now; the
// runner tests thread it through the engine configs they build.
type runnerTestEstimator struct{}

func (runnerTestEstimator) Estimate(s string) int { return len(s)/4 + 1 }

// --- Test runner + event fixtures ---

// newTestRunner builds a minimal Runner with a real SQLite-in-TempDir
// backing store. processEvents doesn't need adkRunner or rrcLLM; the
// other Runner fields stay zero.
func newTestRunner(t *testing.T, threadID string) *Runner {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(dir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.CreateThread(&threadv1.Thread{
		Id: threadID, Name: "test", CreatedAt: timestamppb.Now(),
	}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	return &Runner{
		db:       db,
		threadID: threadID,
		msgSeq:   atomic.Int64{},
	}
}

// eventSeq builds an iter.Seq2 from a slice of events. Every event
// yields with nil error.
func eventSeq(events ...*session.Event) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, e := range events {
			if !yield(e, nil) {
				return
			}
		}
	}
}

// eventSeqThenError yields the given events, then yields (nil, err).
// Mirrors ADK iterator behavior when a stream errors mid-turn —
// prior events were committed before the error surfaced.
func eventSeqThenError(err error, events ...*session.Event) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, e := range events {
			if !yield(e, nil) {
				return
			}
		}
		yield(nil, err)
	}
}

func fnCallEvent(id, name string, args map[string]any) *session.Event {
	return &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{ID: id, Name: name, Args: args},
				}},
			},
		},
	}
}

func fnResponseEvent(id, name string, resp map[string]any) *session.Event {
	return &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role: "user",
				Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{ID: id, Name: name, Response: resp},
				}},
			},
		},
	}
}

func textEvent(text string, thought bool) *session.Event {
	return &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: text, Thought: thought}},
			},
		},
	}
}

// thinkingChunkEvent builds one thinking Part carrying an optional
// signature — mirrors what the bridge yields per streamed thinking
// chunk: a text-bearing part with no signature, or (per Anthropic's
// zero-text terminator) an empty-text part carrying only the
// signature that closes the block.
func thinkingChunkEvent(text string, signature []byte) *session.Event {
	return &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: text, Thought: true, ThoughtSignature: signature}},
			},
		},
	}
}

// fnCallEventSigned is fnCallEvent with a Gemini-style Part-level
// thought signature attached to the function call.
func fnCallEventSigned(id, name string, args map[string]any, signature []byte) *session.Event {
	return &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{
					FunctionCall:     &genai.FunctionCall{ID: id, Name: name, Args: args},
					ThoughtSignature: signature,
				}},
			},
		},
	}
}

// corpusOf returns the stored corpus for the runner's thread. Helper
// shared by tests that check message ordering and dedup.
func corpusOf(t *testing.T, r *Runner) []*threadv1.Message {
	t.Helper()
	msgs, err := r.db.ThreadCorpus(r.threadID)
	if err != nil {
		t.Fatalf("ThreadCorpus: %v", err)
	}
	return msgs
}

// --- Tests ---

func TestProcessEvents_DuplicateFunctionCallDedup(t *testing.T) {
	// ADK sometimes re-emits the same FunctionCall across events.
	// Storage must dedup on ID so the corpus the RRC engine sees
	// next round doesn't contain phantom retries.
	r := newTestRunner(t, "thread-1")
	call := fnCallEvent("c1", "Bash", map[string]any{"cmd": "ls"})

	events := eventSeq(call, call, call) // same event thrice
	_, _ = r.processEvents(context.Background(), events)

	corpus := corpusOf(t, r)
	count := 0
	for _, m := range corpus {
		for _, b := range m.Content {
			if b.GetToolCall() != nil {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("duplicate FunctionCall should be stored once, got %d", count)
	}
}

func TestProcessEvents_DuplicateFunctionResponseDedup(t *testing.T) {
	// ADK sometimes re-emits the same FunctionResponse. A call plus its
	// duplicated response must yield exactly one stored result. (A
	// response with no call is dropped, not stored — see
	// OrphanFunctionResponseDropped.)
	r := newTestRunner(t, "thread-1")
	events := eventSeq(
		fnCallEvent("c1", "Bash", map[string]any{"cmd": "ls"}),
		fnResponseEvent("c1", "Bash", map[string]any{"output": "out"}),
		fnResponseEvent("c1", "Bash", map[string]any{"output": "out"}), // dup
	)
	_, _ = r.processEvents(context.Background(), events)

	corpus := corpusOf(t, r)
	count := 0
	for _, m := range corpus {
		for _, b := range m.Content {
			if b.GetToolResult() != nil {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("duplicate FunctionResponse should be stored once, got %d", count)
	}
}

func TestProcessEvents_UnmatchedToolCallGetsExactErrorResult(t *testing.T) {
	r := newTestRunner(t, "t-unmatched")
	_, err := r.processEvents(context.Background(), eventSeqThenError(
		errors.New("cancelled"),
		fnCallEvent("call-1", "Read", map[string]any{"path": "x"}),
	))
	if err == nil {
		t.Fatal("interrupted run should still report its error")
	}
	corpus := corpusOf(t, r)
	if len(corpus) != 2 {
		t.Fatalf("stored messages=%d, want call plus exact error result", len(corpus))
	}
	call := corpus[0].Content[0].GetToolCall()
	result := corpus[1].Content[0].GetToolResult()
	if call == nil || result == nil || call.Id != result.ToolCallId {
		t.Fatalf("protocol relation not preserved: call=%+v result=%+v", call, result)
	}
	if !result.IsError {
		t.Fatal("synthesized interruption result must be marked as an error")
	}
}

func TestProcessEvents_UserStopPersistsNothingAndReturnsErrStopped(t *testing.T) {
	// A user stop cancels turnCtx. Unlike a plain tool error (above), the
	// cancellation "result" ADK reflects back (e.g. "approval: context
	// canceled" from a pending Bash approval) must NOT be persisted into the
	// corpus, and the turn must report the clean ErrStopped sentinel rather
	// than a surfaced "agent error".
	r := newTestRunner(t, "t-stop")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the stop happened before these events are drained

	resp := fnResponseEvent("call-1", "Bash", map[string]any{"output": "approval: context canceled", "exit_code": 1})
	_, err := r.processEvents(ctx, eventSeq(resp))

	if !errors.Is(err, ErrStopped) {
		t.Fatalf("a cancelled turn must return ErrStopped, got %v", err)
	}
	if corpus := corpusOf(t, r); len(corpus) != 0 {
		t.Fatalf("a user stop must persist no tool_result, got %d messages: %+v", len(corpus), corpus)
	}
}

func TestProcessEvents_UserStopClosesProtocolForDanglingCall(t *testing.T) {
	// The regression guard: a user stop cancels the turn AFTER a tool call was
	// persisted but before its result. Protocol closure requires every persisted
	// call to have a result, so the synthetic backfill must still run on cancel.
	// An earlier version skipped it on cancel, leaving a dangling call that broke
	// every later turn's assembly with "requires exact tool result".
	r := newTestRunner(t, "t-stop-dangling")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := r.processEvents(ctx, eventSeq(fnCallEvent("call-1", "Bash", map[string]any{"cmd": "ls"})))
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("cancelled turn must return ErrStopped, got %v", err)
	}
	var haveCall, haveResult bool
	for _, m := range corpusOf(t, r) {
		for _, b := range m.Content {
			if b.GetToolCall() != nil {
				haveCall = true
			}
			if tr := b.GetToolResult(); tr != nil && tr.ToolCallId == "call-1" {
				haveResult = true
			}
		}
	}
	if !haveCall || !haveResult {
		t.Fatalf("a cancelled tool call must be protocol-closed (call=%v result=%v); a dangling call breaks later turns", haveCall, haveResult)
	}
}

func TestProcessEvents_ThinkingFlushedBeforeToolCall(t *testing.T) {
	// Ordering invariant: when thinking accumulates and then a tool
	// call arrives, the thinking must be stored as its own message
	// BEFORE the tool call. Wrong order here breaks RRC's
	// prerequisite detection on tool chains. Turn ends on a final
	// text event so processEvents can return a message (no "no
	// response from agent" at the tail).
	r := newTestRunner(t, "thread-1")
	events := eventSeq(
		textEvent("I should search the code", true), // thinking
		fnCallEvent("c1", "Grep", map[string]any{"pattern": "foo"}),
		fnResponseEvent("c1", "Grep", map[string]any{"output": "match.go"}),
		textEvent("done", false),
	)

	_, err := r.processEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("processEvents: %v", err)
	}

	corpus := corpusOf(t, r)
	if len(corpus) < 4 {
		t.Fatalf("expected thinking + tool_call + tool_result + final, got %d", len(corpus))
	}
	// First stored: thinking (flushed before tool call)
	if corpus[0].Content[0].GetThinking() == nil {
		t.Fatalf("first stored message should be thinking, got %T", corpus[0].Content[0].Block)
	}
	// Second: tool_call
	if corpus[1].Content[0].GetToolCall() == nil {
		t.Fatalf("second stored message should be tool_call, got %T", corpus[1].Content[0].Block)
	}
	// Third: tool_result
	if corpus[2].Content[0].GetToolResult() == nil {
		t.Fatalf("third stored message should be tool_result, got %T", corpus[2].Content[0].Block)
	}
	// Fourth: final assistant text
	if corpus[3].Content[0].GetText() == nil {
		t.Fatalf("fourth stored message should be final text, got %T", corpus[3].Content[0].Block)
	}
}

func TestProcessEvents_TextAndThinkingAccumulateSeparately(t *testing.T) {
	// Parts with Thought=true accumulate into thinkingBuf; Thought=false
	// into textBuf. Both flush at turn-end into a single final
	// assistant message with two blocks.
	r := newTestRunner(t, "thread-1")
	events := eventSeq(
		textEvent("reasoning ", true),
		textEvent("more reasoning. ", true),
		textEvent("final answer: ", false),
		textEvent("42.", false),
	)

	msg, err := r.processEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("processEvents: %v", err)
	}
	if msg == nil {
		t.Fatal("expected a final assistant message")
	}
	if len(msg.Content) != 2 {
		t.Fatalf("final message should have thinking + text blocks, got %d", len(msg.Content))
	}
	if tk := msg.Content[0].GetThinking(); tk == nil || tk.Text != "reasoning more reasoning. " {
		t.Fatalf("thinking text wrong: %+v", msg.Content[0])
	}
	if tx := msg.Content[1].GetText(); tx == nil || tx.Text != "final answer: 42." {
		t.Fatalf("response text wrong: %+v", msg.Content[1])
	}
}

// A signature binds to the exact text accumulated when it arrives —
// two independently signed thinking segments must become two
// separately stored messages, each with its own signature, not one
// merged message under either signature.
func TestProcessEvents_SignatureTriggersThinkingFlush(t *testing.T) {
	r := newTestRunner(t, "thread-1")
	events := eventSeq(
		thinkingChunkEvent("first reasoning", nil),
		thinkingChunkEvent("", []byte("sig-1")),
		thinkingChunkEvent("second reasoning", nil),
		thinkingChunkEvent("", []byte("sig-2")),
		textEvent("done", false),
	)

	msg, err := r.processEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("processEvents: %v", err)
	}
	if msg == nil || msg.Content[0].GetText() == nil || msg.Content[0].GetText().Text != "done" {
		t.Fatalf("expected final text message \"done\", got %+v", msg)
	}

	corpus := corpusOf(t, r)
	if len(corpus) != 3 {
		t.Fatalf("expected 2 signed thinking messages + 1 final text, got %d: %+v", len(corpus), corpus)
	}
	th1 := corpus[0].Content[0].GetThinking()
	if th1 == nil || th1.Text != "first reasoning" || string(th1.Signature) != "sig-1" {
		t.Fatalf("first thinking block wrong: %+v", th1)
	}
	th2 := corpus[1].Content[0].GetThinking()
	if th2 == nil || th2.Text != "second reasoning" || string(th2.Signature) != "sig-2" {
		t.Fatalf("second thinking block wrong: %+v", th2)
	}
	if corpus[2].Content[0].GetText() == nil {
		t.Fatalf("third stored message should be the final text, got %+v", corpus[2])
	}
}

// A Gemini function call's Part-level thought signature is stored on
// the persisted ToolCallContent block.
func TestProcessEvents_ToolCallSignatureStored(t *testing.T) {
	r := newTestRunner(t, "thread-1")
	events := eventSeq(
		fnCallEventSigned("c1", "Bash", map[string]any{"cmd": "ls"}, []byte("call-sig")),
		fnResponseEvent("c1", "Bash", map[string]any{"output": "out"}),
		textEvent("done", false),
	)

	_, err := r.processEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("processEvents: %v", err)
	}

	corpus := corpusOf(t, r)
	call := corpus[0].Content[0].GetToolCall()
	if call == nil || string(call.Signature) != "call-sig" {
		t.Fatalf("tool_call signature not stored: %+v", call)
	}
}

// Thinking that never receives a signature accumulates exactly as
// before — no premature flush, no signature on the eventual message.
func TestProcessEvents_UnsignedThinkingAccumulatesAsBefore(t *testing.T) {
	r := newTestRunner(t, "thread-1")
	events := eventSeq(
		textEvent("reasoning", true),
		textEvent("more", true),
	)

	msg, err := r.processEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("processEvents: %v", err)
	}
	th := msg.Content[0].GetThinking()
	if th == nil || th.Text != "reasoningmore" || len(th.Signature) != 0 {
		t.Fatalf("unsigned thinking should accumulate with no signature: %+v", th)
	}
}

func TestProcessEvents_ContentWinsOverLastErr(t *testing.T) {
	// If content accumulated AND the iterator errored, content path
	// wins — the message is returned, error is swallowed. Represents
	// "partial response arrived before the stream broke."
	r := newTestRunner(t, "thread-1")
	boom := errors.New("stream broken after content")
	events := eventSeqThenError(boom, textEvent("partial answer", false))

	msg, err := r.processEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("content should win over lastErr, got err: %v", err)
	}
	if msg == nil {
		t.Fatal("expected final assistant message")
	}
	if msg.Content[0].GetText().Text != "partial answer" {
		t.Fatalf("wrong text: %+v", msg.Content[0])
	}
}

func TestProcessEvents_ErrorWithNoContent(t *testing.T) {
	// Iterator errors and nothing accumulated → "agent error"
	// wrapping lastErr. Distinct from "no response from agent"
	// (which fires when neither content nor error came through).
	r := newTestRunner(t, "thread-1")
	boom := errors.New("total failure")
	events := eventSeqThenError(boom)

	msg, err := r.processEvents(context.Background(), events)
	if msg != nil {
		t.Fatalf("expected nil message, got %+v", msg)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error should wrap the iterator error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "agent error") {
		t.Fatalf("error should be wrapped as 'agent error', got: %v", err)
	}
}

func TestProcessEvents_NoEventsReturnsError(t *testing.T) {
	// Empty iterator, no error → "no response from agent".
	r := newTestRunner(t, "thread-1")
	events := eventSeq()

	msg, err := r.processEvents(context.Background(), events)
	if msg != nil {
		t.Fatalf("expected nil message, got %+v", msg)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no response from agent") {
		t.Fatalf("expected 'no response from agent', got: %v", err)
	}
}

func TestProcessEvents_NilContentSkipped(t *testing.T) {
	// Events with nil Content are ignored (continue), not errors.
	// Mirrors ADK's heartbeat/empty events during streaming.
	r := newTestRunner(t, "thread-1")
	nilContentEvent := &session.Event{LLMResponse: model.LLMResponse{Content: nil}}
	events := eventSeq(nilContentEvent, textEvent("after empty", false))

	msg, err := r.processEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("nil-content event should be skipped silently: %v", err)
	}
	if msg == nil || msg.Content[0].GetText().Text != "after empty" {
		t.Fatalf("expected 'after empty', got %+v", msg)
	}
}

func TestNextMsgID_NanosecondAndCounterUnique(t *testing.T) {
	// nextMsgID uses nanosecond timestamp + atomic counter. Back-to-back
	// calls within the same nanosecond tick must still yield distinct
	// IDs via the counter suffix.
	r := newTestRunner(t, "thread-1")
	ids := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := r.nextMsgID()
		if ids[id] {
			t.Fatalf("duplicate id on iteration %d: %s", i, id)
		}
		ids[id] = true
	}
}

func TestProcessEvents_MultipleDistinctToolCalls(t *testing.T) {
	// Distinct tool_call IDs across events are all stored — dedup is
	// strictly by ID, not by presence.
	r := newTestRunner(t, "thread-1")
	events := eventSeq(
		fnCallEvent("c1", "Bash", nil),
		fnResponseEvent("c1", "Bash", map[string]any{"output": "one"}),
		fnCallEvent("c2", "Grep", nil),
		fnResponseEvent("c2", "Grep", map[string]any{"output": "two"}),
	)

	_, _ = r.processEvents(context.Background(), events)
	corpus := corpusOf(t, r)
	calls, results := 0, 0
	for _, m := range corpus {
		for _, b := range m.Content {
			if b.GetToolCall() != nil {
				calls++
			}
			if b.GetToolResult() != nil {
				results++
			}
		}
	}
	if calls != 2 || results != 2 {
		t.Fatalf("expected 2 calls + 2 results, got calls=%d results=%d", calls, results)
	}
}

func TestProcessEvents_OnToolCallCallback(t *testing.T) {
	// The OnToolCall/OnToolResult hooks fire once per stored call/result,
	// not once per re-emission. Verify via call counts under a dedup
	// scenario.
	r := newTestRunner(t, "thread-1")
	callCount, resultCount := 0, 0
	r.OnToolCall = func(_, _, _ string) { callCount++ }
	r.OnToolResult = func(_, _, _ string, _ bool) { resultCount++ }

	events := eventSeq(
		fnCallEvent("c1", "Bash", nil),
		fnCallEvent("c1", "Bash", nil), // dup
		fnResponseEvent("c1", "Bash", map[string]any{"output": "x"}),
		fnResponseEvent("c1", "Bash", map[string]any{"output": "x"}), // dup
	)
	_, _ = r.processEvents(context.Background(), events)

	if callCount != 1 {
		t.Fatalf("OnToolCall should fire once per unique ID, got %d", callCount)
	}
	if resultCount != 1 {
		t.Fatalf("OnToolResult should fire once per unique ID, got %d", resultCount)
	}
}

func TestProcessEvents_OrphanFunctionResponseDropped(t *testing.T) {
	// A FunctionResponse with no matching (buffered) tool_call is
	// unpairable — persisting it would brick CloseGroup ("requires exact
	// tool call"). It must be dropped, not stored. (Cannot occur in the
	// real ADK loop; every response follows a call.)
	r := newTestRunner(t, "thread-1")
	_, _ = r.processEvents(context.Background(), eventSeq(
		fnResponseEvent("c1", "Bash", map[string]any{"output": "out"}),
	))
	if corpus := corpusOf(t, r); len(corpus) != 0 {
		t.Fatalf("orphan tool_result must be dropped, got %d messages: %+v", len(corpus), corpus)
	}
}

func TestProcessEvents_ParallelToolCallsGroupedAndClosed(t *testing.T) {
	// Two parallel tool calls (both emitted before either result) persist
	// as a GROUPED, fully-closed corpus — call1, call2, result1, result2
	// in position order, byte-identical to the pre-change shape, because
	// positions are stamped at emission, not at insert. Each call pairs
	// with its result.
	r := newTestRunner(t, "thread-1")
	events := eventSeq(
		fnCallEvent("c1", "Bash", map[string]any{"cmd": "ls"}),
		fnCallEvent("c2", "Grep", map[string]any{"pattern": "foo"}),
		fnResponseEvent("c1", "Bash", map[string]any{"output": "one"}),
		fnResponseEvent("c2", "Grep", map[string]any{"output": "two"}),
		textEvent("done", false),
	)
	if _, err := r.processEvents(context.Background(), events); err != nil {
		t.Fatalf("processEvents: %v", err)
	}
	var kinds []string
	for _, m := range corpusOf(t, r) {
		b := m.Content[0]
		switch {
		case b.GetToolCall() != nil:
			kinds = append(kinds, "call:"+b.GetToolCall().Id)
		case b.GetToolResult() != nil:
			kinds = append(kinds, "result:"+b.GetToolResult().ToolCallId)
		case b.GetText() != nil:
			kinds = append(kinds, "text")
		default:
			kinds = append(kinds, "other")
		}
	}
	want := []string{"call:c1", "call:c2", "result:c1", "result:c2", "text"}
	if len(kinds) != len(want) {
		t.Fatalf("corpus shape = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("corpus[%d] = %s, want %s (full: %v)", i, kinds[i], want[i], kinds)
		}
	}
}

func TestProcessEvents_EmptyToolCallIDFailsFast(t *testing.T) {
	// An empty tool-call id is unpairable — the turn must fail loudly
	// rather than silently drop the call or persist an unpairable one.
	r := newTestRunner(t, "thread-1")
	_, err := r.processEvents(context.Background(), eventSeq(
		fnCallEvent("", "Bash", map[string]any{"cmd": "ls"}),
	))
	if err == nil {
		t.Fatal("empty tool-call id must fail the turn")
	}
	if corpus := corpusOf(t, r); len(corpus) != 0 {
		t.Fatalf("empty-id call must persist nothing, got %d: %+v", len(corpus), corpus)
	}
}
