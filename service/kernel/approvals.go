package kernel

// Tool-approval + AskUserQuestion answer channel registry. The
// runtime owns each channel; the graph mutations (approveToolCall,
// denyToolCall, answerQuestion) send the verdict into it. Channels
// are buffered=1 so a send never blocks even if the runner has
// already moved on.

// RegisterApproval registers a buffered approval channel for a
// pending tool call. Returns the receive side and an unregister
// thunk; callers (the runner factory) defer the unregister.
func (k *Kernel) RegisterApproval(callID, threadID string) (<-chan bool, func()) {
	ch := make(chan bool, 1)
	k.pendingMu.Lock()
	k.pendingApprovals[callID] = ch
	k.pendingThreadIDs[callID] = threadID
	k.pendingMu.Unlock()
	return ch, func() {
		k.pendingMu.Lock()
		delete(k.pendingApprovals, callID)
		delete(k.pendingThreadIDs, callID)
		k.pendingMu.Unlock()
	}
}

// RegisterAnswer registers a buffered answer channel for a pending
// AskUserQuestion. Returns the receive side and an unregister
// thunk.
func (k *Kernel) RegisterAnswer(callID string) (<-chan string, func()) {
	ch := make(chan string, 1)
	k.pendingMu.Lock()
	k.pendingAnswers[callID] = ch
	k.pendingMu.Unlock()
	return ch, func() {
		k.pendingMu.Lock()
		delete(k.pendingAnswers, callID)
		k.pendingMu.Unlock()
	}
}

// SendApproval delivers an approval/denial verdict to the waiting
// goroutine. Returns false if no approval is pending under callID
// (stale answer, expired wait). Used by graph
// approveToolCall/denyToolCall mutations.
func (k *Kernel) SendApproval(callID string, approved bool) bool {
	k.pendingMu.Lock()
	ch, ok := k.pendingApprovals[callID]
	k.pendingMu.Unlock()
	if !ok {
		return false
	}
	ch <- approved
	return true
}

// SendAnswer delivers a question answer to the waiting AskUser
// goroutine. Returns false if no question is pending under callID.
// Used by graph answerQuestion mutation.
func (k *Kernel) SendAnswer(callID, answer string) bool {
	k.pendingMu.Lock()
	ch, ok := k.pendingAnswers[callID]
	k.pendingMu.Unlock()
	if !ok {
		return false
	}
	ch <- answer
	return true
}

// ThreadIDForCall returns the thread id associated with a pending
// approval, used by denyToolCall to synthesize the per-thread
// denial-reason system message.
func (k *Kernel) ThreadIDForCall(callID string) string {
	k.pendingMu.Lock()
	defer k.pendingMu.Unlock()
	return k.pendingThreadIDs[callID]
}
