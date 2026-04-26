// Package runner provides the per-thread runner registry the
// runtime kernel uses to manage active agent runners and their
// lifecycle. The registry is the storage primitive — lookup,
// insert, delete, atomic per-entry mutation, and snapshot
// iteration. Higher-level concerns (subagent merge on stop,
// cancel-and-close on shutdown) live with their callers because
// they reach into other kernel surfaces (pubsub, agent runner
// itself).
//
// The factory that constructs a Runner from a thread id + config
// — formerly the 450-LOC getOrCreateRunner closure on
// graph.Resolver — also lives in this package alongside the
// per-family Deps option groups. Subsequent commits land that
// piece; this commit is the registry primitive only.
package runner

import (
	"context"
	"sync"

	"github.com/emontenegr/spidey/service/agent"
)

// Entry is a live runner with its lifecycle handles.
type Entry struct {
	Runner         *agent.Runner
	Cancel         context.CancelFunc       // autonomous-loop cancel; nil for normal threads
	ParentThreadID string                   // non-empty for subagent forks — merge on stop
	AskCh          chan agent.AskRequest    // closed on stop to terminate the ask goroutine
}

// Registry is a thread-safe map from thread id to Entry. Every
// access — Get, Set, Delete, SetCancel — takes the registry's
// lock; iteration via Range snapshots under the lock and runs
// the callback after release so callers can call back into
// other registry methods without deadlocking.
type Registry struct {
	mu sync.Mutex
	m  map[string]*Entry
}

// NewRegistry constructs an empty registry.
func NewRegistry() *Registry {
	return &Registry{m: map[string]*Entry{}}
}

// Get returns the entry for threadID. Second return is false
// when no entry exists.
func (r *Registry) Get(threadID string) (*Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[threadID]
	return e, ok
}

// Set stores e under threadID, replacing any existing entry.
// Caller is responsible for stopping the prior entry if one
// existed — Set does not.
func (r *Registry) Set(threadID string, e *Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[threadID] = e
}

// SetCancel updates Cancel on an existing entry. No-op if no
// entry is registered under threadID. Used by the boot
// reconciler to attach an autonomous-loop cancel func to a
// runner that was already constructed by the factory.
func (r *Registry) SetCancel(threadID string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.m[threadID]; ok {
		e.Cancel = cancel
	}
}

// Delete removes the entry for threadID. The caller must have
// already stopped the runner (cancel context, close AskCh) —
// Delete only updates the map. No-op if no entry exists.
func (r *Registry) Delete(threadID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, threadID)
}

// Range invokes fn for every entry. Snapshot is taken under the
// lock; fn runs after release, so it may call back into the
// registry without deadlocking. Iteration order is unspecified.
func (r *Registry) Range(fn func(threadID string, e *Entry)) {
	r.mu.Lock()
	snapshot := make(map[string]*Entry, len(r.m))
	for k, v := range r.m {
		snapshot[k] = v
	}
	r.mu.Unlock()
	for k, v := range snapshot {
		fn(k, v)
	}
}

// StopAll cancels and closes every entry, then empties the
// registry. Used by ReloadProviders when settings change —
// every active runner is holding a stale provider reference and
// must be torn down so the next getOrCreateRunner call rebuilds
// against the new substrate.
func (r *Registry) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for tid, e := range r.m {
		if e.Cancel != nil {
			e.Cancel()
		}
		if e.AskCh != nil {
			close(e.AskCh)
		}
		delete(r.m, tid)
	}
}
