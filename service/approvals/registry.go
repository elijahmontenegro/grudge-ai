// Package approvals owns the tool-approval + AskUserQuestion answer
// channel registry. The runtime owns each channel; the graph
// mutations (approveToolCall, denyToolCall, answerQuestion) send the
// verdict into it. Channels are buffered=1 so a send never blocks
// even if the runner has already moved on.
package approvals

import "sync"

// Registry holds the in-flight approval and answer channels keyed by
// callID. Construct via New.
type Registry struct {
	mu        sync.Mutex
	approvals map[string]chan bool
	answers   map[string]chan string
	threadIDs map[string]string
}

// New constructs an empty Registry.
func New() *Registry {
	return &Registry{
		approvals: make(map[string]chan bool),
		answers:   make(map[string]chan string),
		threadIDs: make(map[string]string),
	}
}

// RegisterApproval registers a buffered approval channel for a
// pending tool call. Returns the receive side and an unregister
// thunk; callers (the runner factory) defer the unregister.
func (r *Registry) RegisterApproval(callID, threadID string) (<-chan bool, func()) {
	ch := make(chan bool, 1)
	r.mu.Lock()
	r.approvals[callID] = ch
	r.threadIDs[callID] = threadID
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.approvals, callID)
		delete(r.threadIDs, callID)
		r.mu.Unlock()
	}
}

// RegisterAnswer registers a buffered answer channel for a pending
// AskUserQuestion. Returns the receive side and an unregister thunk.
func (r *Registry) RegisterAnswer(callID string) (<-chan string, func()) {
	ch := make(chan string, 1)
	r.mu.Lock()
	r.answers[callID] = ch
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.answers, callID)
		r.mu.Unlock()
	}
}

// SendApproval delivers an approval/denial verdict to the waiting
// goroutine. Returns false if no approval is pending under callID
// (stale answer, expired wait). Used by graph
// approveToolCall/denyToolCall mutations.
func (r *Registry) SendApproval(callID string, approved bool) bool {
	r.mu.Lock()
	ch, ok := r.approvals[callID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	ch <- approved
	return true
}

// SendAnswer delivers a question answer to the waiting AskUser
// goroutine. Returns false if no question is pending under callID.
// Used by graph answerQuestion mutation.
func (r *Registry) SendAnswer(callID, answer string) bool {
	r.mu.Lock()
	ch, ok := r.answers[callID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	ch <- answer
	return true
}

// ThreadIDForCall returns the thread id associated with a pending
// approval, used by denyToolCall to synthesize the per-thread
// denial-reason system message.
func (r *Registry) ThreadIDForCall(callID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.threadIDs[callID]
}
