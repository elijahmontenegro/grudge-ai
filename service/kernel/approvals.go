package kernel

// Tool-approval + AskUserQuestion answer channel delegation. Kernel
// holds an *approvals.Registry (see kernel.go); these wrappers
// preserve the resolver-side API while the channel maps live in the
// focused service/approvals package.

// RegisterApproval registers a buffered approval channel for a
// pending tool call. Returns the receive side and an unregister
// thunk; callers (the runner factory) defer the unregister.
func (k *Kernel) RegisterApproval(callID, threadID string) (<-chan bool, func()) {
	return k.Approvals.RegisterApproval(callID, threadID)
}

// RegisterAnswer registers a buffered answer channel for a pending
// AskUserQuestion. Returns the receive side and an unregister thunk.
func (k *Kernel) RegisterAnswer(callID string) (<-chan string, func()) {
	return k.Approvals.RegisterAnswer(callID)
}

// SendApproval delivers an approval/denial verdict to the waiting
// goroutine. Returns false if no approval is pending under callID.
// Used by graph approveToolCall/denyToolCall mutations.
func (k *Kernel) SendApproval(callID string, approved bool) bool {
	return k.Approvals.SendApproval(callID, approved)
}

// SendAnswer delivers a question answer to the waiting AskUser
// goroutine. Returns false if no question is pending under callID.
// Used by graph answerQuestion mutation.
func (k *Kernel) SendAnswer(callID, answer string) bool {
	return k.Approvals.SendAnswer(callID, answer)
}

// ThreadIDForCall returns the thread id associated with a pending
// approval, used by denyToolCall to synthesize the per-thread
// denial-reason system message.
func (k *Kernel) ThreadIDForCall(callID string) string {
	return k.Approvals.ThreadIDForCall(callID)
}
