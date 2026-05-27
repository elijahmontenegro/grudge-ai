package kernel

// Plan-content cache delegation. Kernel holds a *plans.Cache (see
// kernel.go); these wrappers preserve the resolver-side API while
// the state lives in the focused service/plans package.

// GetPlan returns the cached plan content for a thread, or empty
// if none is set.
func (k *Kernel) GetPlan(threadID string) string { return k.Plans.Get(threadID) }

// SetPlan stores plan content for a thread, overwriting any prior
// content.
func (k *Kernel) SetPlan(threadID, content string) { k.Plans.Set(threadID, content) }

// ClearPlan removes any cached plan content for a thread.
// approvePlan / rejectPlan call this to drop the artifact.
func (k *Kernel) ClearPlan(threadID string) { k.Plans.Clear(threadID) }
