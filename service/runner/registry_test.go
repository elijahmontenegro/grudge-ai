package runner

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

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
