package graph

import (
	"context"
	"log"
	"time"

	"github.com/emontenegr/spidey/service/runtime/agentstate"
	"github.com/emontenegr/spidey/service/storage"
)

// ReconcileOnBoot normalizes agent_state rows that were left non-Idle
// when the service last exited. Goroutines don't survive a process
// restart (power loss, intentional restart, crash), so any row marked
// Running or Paused is referencing an in-memory runner that no longer
// exists — queries return a stale "RUNNING/AUTONOMOUS" to the UI
// while nothing is actually producing output.
//
// Policy per row:
//
//   - Running + Autonomous with enough metadata + time remaining:
//     auto-resume. Matches spec (spec/web/MANIFEST.adoc:206 — "Running
//     autonomous agents resume or show their last state"). Launches a
//     fresh RunAutonomous goroutine with a "continue" kickoff and the
//     remaining-duration budget (total - elapsed, floored at 5 min).
//
//   - Running/Paused + Autonomous with missing metadata or expired:
//     reset to Idle. Without a valid started_at we can't compute a
//     truthful remaining budget; rather than invent one we bring the
//     row to a truthful quiescent state and let the user restart
//     explicitly. Same for expired runs.
//
//   - Running + Plan or Normal: reset to Idle. Plan/normal modes
//     aren't loops — nothing to resume; the Running flag was carry
//     from an in-flight turn that obviously didn't finish.
//
//   - Paused + anything non-autonomous: reset to Idle. Pause only
//     has semantic for autonomous loops.
//
// Publishes corrected states to any live subscriptions (early
// browsers connecting post-restart see the correct value rather
// than the stale DB one).
func (r *Resolver) ReconcileOnBoot(ctx context.Context) {
	rows, err := r.DB.ListActiveAgentStates()
	if err != nil {
		log.Printf("[Reconcile] list active states: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	log.Printf("[Reconcile] inspecting %d non-idle agent_state row(s) from prior session", len(rows))

	for _, st := range rows {
		action := r.reconcileRow(ctx, st)
		log.Printf("[Reconcile] thread=%s status=%d mode=%d → %s",
			st.ThreadID, st.Status, st.Mode, action)
	}
}

// reconcileRow handles one agent_state row and returns a short description
// of what was done, for logging.
func (r *Resolver) reconcileRow(ctx context.Context, st *storage.AgentState) string {
	// Only autonomous runs are eligible for auto-resume. Everything
	// else gets normalized to Idle.
	if st.Mode != storage.AgentModeAutonomous {
		r.resetToIdle(st.ThreadID)
		return "reset to idle (non-autonomous)"
	}

	// Autonomous + Paused: user explicitly paused before shutdown.
	// The runner's in-memory pause gate is gone; we can't resume a
	// "pause" that doesn't exist anymore. Reset to Idle so the user
	// makes an explicit choice (restart via startAutonomous, or
	// leave the thread alone).
	if st.Status == storage.AgentStatusPaused {
		r.resetToIdle(st.ThreadID)
		return "reset to idle (paused, runner gone)"
	}

	// Autonomous + Running: try to auto-resume.
	remaining, reason := agentstate.ComputeRemainingBudget(st)
	if reason != "" {
		r.resetToIdle(st.ThreadID)
		return "reset to idle (" + reason + ")"
	}

	runner, rerr := r.getOrCreateRunner(st.ThreadID)
	if rerr != nil {
		log.Printf("[Reconcile] getOrCreateRunner(%s): %v", st.ThreadID, rerr)
		r.resetToIdle(st.ThreadID)
		return "reset to idle (runner rebuild failed)"
	}

	autoCtx, autoCancel := context.WithCancel(context.Background())
	r.Runners.SetCancel(st.ThreadID, autoCancel)

	// Keep the DB row's Running/Autonomous status intact — the
	// resumed goroutine will maintain it. Publish to subscribers so
	// any freshly-connected browsers see truthful state.
	r.publishAgentState(st.ThreadID, &AgentState{
		ThreadID: st.ThreadID, Status: AgentStatusRunning, Mode: AgentModeAutonomous,
	})

	threadID := st.ThreadID
	go func() {
		defer r.stopRunner(threadID)
		// Narrow UPDATE (status + mode only) instead of full-row
		// UPSERT. SaveAgentState would zero duration_limit and
		// started_at, losing audit-trail info about the run that
		// just ended. SetAgentStatusAndMode leaves those columns
		// intact — the row remains a truthful record that "this
		// thread previously ran autonomously for 24h starting at T"
		// even after exit. Identical to the pattern used by
		// resetToIdle below for the exact same reason.
		defer func() {
			if err := r.DB.SetAgentStatusAndMode(threadID, storage.AgentStatusIdle, storage.AgentModeNormal); err != nil {
				log.Printf("[Reconcile] SetAgentStatusAndMode(%s): %v", threadID, err)
			}
		}()
		defer r.publishAgentState(threadID, &AgentState{
			ThreadID: threadID, Status: AgentStatusIdle, Mode: AgentModeNormal,
		})
		if err := runner.RunAutonomous(autoCtx, "continue", remaining); err != nil {
			log.Printf("[Reconcile] RunAutonomous(%s): %v", threadID, err)
		}
	}()
	return "auto-resumed autonomous (" + remaining.Round(time.Second).String() + " remaining)"
}

// resetToIdle normalizes the DB row and publishes the corrected state.
// Narrow Update (not SaveAgentState) so we don't need to carry the other
// columns around — SetAgentStatusAndMode leaves round_count / started_at /
// duration_limit intact as historical markers, which is harmless and
// preserves audit trail.
func (r *Resolver) resetToIdle(threadID string) {
	if err := r.DB.SetAgentStatusAndMode(threadID, storage.AgentStatusIdle, storage.AgentModeNormal); err != nil {
		log.Printf("[Reconcile] reset %s to idle: %v", threadID, err)
		return
	}
	r.publishAgentState(threadID, &AgentState{
		ThreadID: threadID, Status: AgentStatusIdle, Mode: AgentModeNormal,
	})
}

// (computeRemainingBudget moved to service/runtime/agentstate.)

