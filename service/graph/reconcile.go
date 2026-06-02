package graph

import (
	"context"

	"github.com/elijahmontenegro/grudge/service/runtime"
)

// ReconcileOnBoot is the thin graph-side entry point the boot
// caller (cmd/grudge/main.go) invokes. The actual reconciliation
// logic — agent_state row inspection, auto-resume, normalize-to-idle
// — lives on service/runtime; this shim wraps it with the graph
// Resolver's runtimeDeps so the call-site doesn't have to construct
// Deps itself.
func (r *Resolver) ReconcileOnBoot(ctx context.Context) {
	runtime.ReconcileOnBoot(ctx, r.runtimeDeps())
}
