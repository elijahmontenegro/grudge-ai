package kernel

// Plan-content cache. The ExitPlan tool writes the compiled plan
// here at the end of plan mode; AgentState publishes attach it to
// outgoing events so the UI's plan panel renders without an extra
// round-trip. State is in-memory; the canonical plan source lives
// on disk under {DataDir}/plans/plan-{threadID}/.

// GetPlan returns the cached plan content for a thread, or empty
// if none is set.
func (k *Kernel) GetPlan(threadID string) string {
	k.planContentMu.RLock()
	defer k.planContentMu.RUnlock()
	return k.planContent[threadID]
}

// SetPlan stores plan content for a thread, overwriting any prior
// content.
func (k *Kernel) SetPlan(threadID, content string) {
	k.planContentMu.Lock()
	k.planContent[threadID] = content
	k.planContentMu.Unlock()
}

// ClearPlan removes any cached plan content for a thread.
// approvePlan / rejectPlan call this to drop the artifact.
func (k *Kernel) ClearPlan(threadID string) {
	k.planContentMu.Lock()
	delete(k.planContent, threadID)
	k.planContentMu.Unlock()
}
