package runtime

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/emontenegr/grudge/core/httpc/retry"
)

// fakePubsub records every Publish* call. Used by Registry.Stop
// tests to verify the subagent-completed event fires on fork
// teardown without standing up a real graph resolver.
type fakePubsub struct {
	mu        sync.Mutex
	streams   []StreamDelta
	states    []AgentStateUpdate
	tools     []ToolExec
	subagents []SubagentEvent
	retries   []retry.Event
}

func (f *fakePubsub) PublishStream(ev StreamDelta)         { f.mu.Lock(); defer f.mu.Unlock(); f.streams = append(f.streams, ev) }
func (f *fakePubsub) PublishAgentState(ev AgentStateUpdate) { f.mu.Lock(); defer f.mu.Unlock(); f.states = append(f.states, ev) }
func (f *fakePubsub) PublishToolExec(ev ToolExec)          { f.mu.Lock(); defer f.mu.Unlock(); f.tools = append(f.tools, ev) }
func (f *fakePubsub) PublishSubagent(ev SubagentEvent)     { f.mu.Lock(); defer f.mu.Unlock(); f.subagents = append(f.subagents, ev) }
func (f *fakePubsub) PublishRetry(threadID string, ev retry.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retries = append(f.retries, ev)
}

// TestGetOrBuild_SingleFlight verifies the registry's lookup-then-
// build dance runs the closure exactly once across N concurrent
// callers for the same thread id. Without GetOrBuild the orphan-
// runner race is real: two callers both Get(nil), both Build, both
// Set — the loser's runner has no parent in the registry and
// stopRunner can't reach it.
func TestGetOrBuild_SingleFlight(t *testing.T) {
	r := NewRegistry()

	var built atomic.Int32
	build := func() (*Entry, error) {
		built.Add(1)
		return &Entry{}, nil
	}

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	results := make([]*Entry, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = r.GetOrBuild("t1", build)
		}(i)
	}
	wg.Wait()

	if got := built.Load(); got != 1 {
		t.Fatalf("build called %d times, want 1", got)
	}
	first := results[0]
	if first == nil {
		t.Fatal("first caller got nil entry")
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d: unexpected err %v", i, errs[i])
		}
		if results[i] != first {
			t.Errorf("caller %d: got entry %p, want %p (single-flight should share)", i, results[i], first)
		}
	}
}

// TestGetOrBuild_FailureRetries — a build failure must not wedge
// the registry as sync.Once would. Subsequent callers retry and
// can succeed once the underlying issue is fixed (settings change,
// provider becomes reachable, etc.).
func TestGetOrBuild_FailureRetries(t *testing.T) {
	r := NewRegistry()

	var attempts atomic.Int32
	failTwice := func() (*Entry, error) {
		n := attempts.Add(1)
		if n <= 2 {
			return nil, errors.New("transient")
		}
		return &Entry{}, nil
	}

	if _, err := r.GetOrBuild("t1", failTwice); err == nil {
		t.Fatal("expected first call to fail")
	}
	if _, err := r.GetOrBuild("t1", failTwice); err == nil {
		t.Fatal("expected second call to fail")
	}
	entry, err := r.GetOrBuild("t1", failTwice)
	if err != nil {
		t.Fatalf("third call should succeed, got %v", err)
	}
	if entry == nil {
		t.Fatal("third call returned nil entry")
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("expected 3 build attempts, got %d", got)
	}
}

// TestGetOrBuild_ExistingEntry — once an entry is registered, a
// subsequent GetOrBuild returns it without invoking build.
func TestGetOrBuild_ExistingEntry(t *testing.T) {
	r := NewRegistry()
	pre := &Entry{}
	r.Set("t1", pre)

	var called atomic.Int32
	build := func() (*Entry, error) {
		called.Add(1)
		return &Entry{}, nil
	}

	got, err := r.GetOrBuild("t1", build)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != pre {
		t.Errorf("expected pre-registered entry, got %p", got)
	}
	if called.Load() != 0 {
		t.Errorf("build should not run when entry exists")
	}
}

// TestRegistry_Stop_MissingThread — Stop on an unregistered thread
// returns false and is a no-op. The graph resolver's stopRunner was
// the only caller-of-record that relied on this silently-skip
// behavior (defer'd stopRunner in autonomous-loop teardown paths
// can fire after a prior Delete).
func TestRegistry_Stop_MissingThread(t *testing.T) {
	r := NewRegistry()
	pubsub := &fakePubsub{}

	if ok := r.Stop("nope", pubsub); ok {
		t.Errorf("Stop on missing thread returned true, want false")
	}
	if len(pubsub.subagents) != 0 {
		t.Errorf("missing-thread Stop emitted %d subagent events, want 0", len(pubsub.subagents))
	}
}

// TestRegistry_Stop_NormalThread — Stop on a present, non-fork
// entry returns true, removes it from the registry, and emits no
// subagent event (no parent to merge into).
func TestRegistry_Stop_NormalThread(t *testing.T) {
	r := NewRegistry()
	pubsub := &fakePubsub{}
	r.Set("t1", &Entry{})

	if ok := r.Stop("t1", pubsub); !ok {
		t.Fatal("Stop on present thread returned false, want true")
	}
	if _, ok := r.Get("t1"); ok {
		t.Errorf("Stop did not remove thread from registry")
	}
	if len(pubsub.subagents) != 0 {
		t.Errorf("non-fork Stop emitted %d subagent events, want 0", len(pubsub.subagents))
	}
}

// TestRegistry_Stop_SubagentFork — Stop on a fork entry (with
// ParentThreadID set) emits one SubagentEvent indicating completion
// to the parent. The merge call is skipped when Runner is nil
// (test setup uses bare Entry); the publish path is the focus of
// this test — the merge path is exercised by the end-to-end smoke.
func TestRegistry_Stop_SubagentFork(t *testing.T) {
	r := NewRegistry()
	pubsub := &fakePubsub{}
	r.Set("parent", &Entry{})
	r.Set("fork", &Entry{ParentThreadID: "parent"})

	if ok := r.Stop("fork", pubsub); !ok {
		t.Fatal("Stop on present fork returned false, want true")
	}
	if _, ok := r.Get("fork"); ok {
		t.Errorf("Stop did not remove fork from registry")
	}
	if got := len(pubsub.subagents); got != 1 {
		t.Fatalf("fork Stop emitted %d subagent events, want 1", got)
	}
	ev := pubsub.subagents[0]
	if ev.ThreadID != "parent" || ev.ForkThreadID != "fork" || ev.Status != "completed" {
		t.Errorf("subagent event = %+v, want {parent, fork, completed}", ev)
	}
}
