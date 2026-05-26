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
	Cancel         context.CancelFunc // autonomous-loop cancel; nil for normal threads
	ParentThreadID string             // non-empty for subagent forks — merge on stop
}

// Registry is a thread-safe map from thread id to Entry. Every
// access — Get, Set, Delete, SetCancel — takes the registry's
// lock; iteration via Range snapshots under the lock and runs
// the callback after release so callers can call back into
// other registry methods without deadlocking.
//
// GetOrBuild handles the lookup-then-construct dance under a
// single critical section so concurrent first-callers for the
// same thread id don't both run the build closure (the orphan-
// runner race: loser's runner has no parent in the registry and
// stopRunner can't reach it).
type Registry struct {
	mu       sync.Mutex
	m        map[string]*Entry
	building map[string]*buildSignal
}

// buildSignal is the per-key channel waiters block on while a
// build closure runs. The channel closes when the build resolves
// (success or failure); err records the failure for waiters.
type buildSignal struct {
	done chan struct{}
	err  error
}

// NewRegistry constructs an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		m:        map[string]*Entry{},
		building: map[string]*buildSignal{},
	}
}

// Get returns the entry for threadID. Second return is false
// when no entry exists.
func (r *Registry) Get(threadID string) (*Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[threadID]
	return e, ok
}

// GetOrBuild returns the entry for threadID, calling build once
// if no entry exists yet. Concurrent callers for the same id
// observe the single build invocation: the first caller runs
// build (with the registry lock released so the closure can do
// I/O); subsequent callers block on a per-key signal until the
// build resolves, then read the entry the first caller stored.
//
// On build failure the signal closes with the recorded err; all
// waiters return the same error and the building entry is
// cleared so the next caller retries (no permanent wedge as a
// sync.Once would impose).
func (r *Registry) GetOrBuild(threadID string, build func() (*Entry, error)) (*Entry, error) {
	for {
		r.mu.Lock()
		if e, ok := r.m[threadID]; ok {
			r.mu.Unlock()
			return e, nil
		}
		if sig, ok := r.building[threadID]; ok {
			r.mu.Unlock()
			<-sig.done
			if sig.err != nil {
				return nil, sig.err
			}
			// Successful build: re-loop to read out of r.m. The
			// builder may have been raced off (Delete called between
			// signal close and our read); in that case the loop
			// observes no entry, no in-flight build, and starts a
			// fresh build itself.
			continue
		}
		sig := &buildSignal{done: make(chan struct{})}
		r.building[threadID] = sig
		r.mu.Unlock()

		entry, err := build()

		r.mu.Lock()
		delete(r.building, threadID)
		if err == nil {
			r.m[threadID] = entry
		}
		sig.err = err
		close(sig.done)
		r.mu.Unlock()
		return entry, err
	}
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
// already stopped the runner (cancel context) — Delete only
// updates the map. No-op if no entry exists.
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
		delete(r.m, tid)
	}
}
