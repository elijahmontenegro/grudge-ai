package agent

import (
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
	_, _ = r.processEvents(events)

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
	r := newTestRunner(t, "thread-1")
	resp := fnResponseEvent("c1", "Bash", map[string]any{"output": "out"})

	events := eventSeq(resp, resp)
	_, _ = r.processEvents(events)

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
	_, err := r.processEvents(eventSeqThenError(
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

	_, err := r.processEvents(events)
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

	msg, err := r.processEvents(events)
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

func TestProcessEvents_ContentWinsOverLastErr(t *testing.T) {
	// If content accumulated AND the iterator errored, content path
	// wins — the message is returned, error is swallowed. Represents
	// "partial response arrived before the stream broke."
	r := newTestRunner(t, "thread-1")
	boom := errors.New("stream broken after content")
	events := eventSeqThenError(boom, textEvent("partial answer", false))

	msg, err := r.processEvents(events)
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

	msg, err := r.processEvents(events)
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

	msg, err := r.processEvents(events)
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

	msg, err := r.processEvents(events)
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

	_, _ = r.processEvents(events)
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
	_, _ = r.processEvents(events)

	if callCount != 1 {
		t.Fatalf("OnToolCall should fire once per unique ID, got %d", callCount)
	}
	if resultCount != 1 {
		t.Fatalf("OnToolResult should fire once per unique ID, got %d", resultCount)
	}
}
