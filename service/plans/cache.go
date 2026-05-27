// Package plans owns the per-thread plan-content cache. The ExitPlan
// tool writes the compiled plan content here; AgentState publishes
// read it back to attach to outgoing events so the UI's plan panel
// renders without an extra round-trip.
//
// State is in-memory; the canonical plan source lives on disk under
// {DataDir}/plans/plan-{threadID}/.
package plans

import "sync"

// Cache holds the in-memory plan content for each thread. The empty
// value is not usable — construct via New.
type Cache struct {
	mu sync.RWMutex
	m  map[string]string
}

// New constructs an empty Cache.
func New() *Cache {
	return &Cache{m: make(map[string]string)}
}

// Get returns the cached plan content for a thread, or empty when
// none is set.
func (c *Cache) Get(threadID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.m[threadID]
}

// Set stores plan content for a thread, overwriting any prior content.
func (c *Cache) Set(threadID, content string) {
	c.mu.Lock()
	c.m[threadID] = content
	c.mu.Unlock()
}

// Clear removes any cached plan content for a thread. approvePlan /
// rejectPlan call this to drop the artifact once the plan has been
// either accepted into normal mode or thrown out.
func (c *Cache) Clear(threadID string) {
	c.mu.Lock()
	delete(c.m, threadID)
	c.mu.Unlock()
}
