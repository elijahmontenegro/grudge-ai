package agent

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
)

// AutonomousState tracks the state of an autonomous run for a thread.
type AutonomousState struct {
	mu       sync.Mutex
	paused   bool
	pauseCh  chan struct{} // closed when pause requested
	resumeCh chan struct{} // closed when resume requested
}

func newAutonomousState() *AutonomousState {
	return &AutonomousState{
		pauseCh:  make(chan struct{}),
		resumeCh: make(chan struct{}),
	}
}

func (a *AutonomousState) Pause() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.paused {
		a.paused = true
		close(a.pauseCh)
	}
}

func (a *AutonomousState) Resume() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.paused {
		a.paused = false
		close(a.resumeCh)
		// Reset channels for next pause/resume cycle
		a.pauseCh = make(chan struct{})
		a.resumeCh = make(chan struct{})
	}
}

func (a *AutonomousState) IsPaused() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.paused
}

// waitIfPaused blocks until resumed or context cancelled. Returns ctx.Err() if cancelled.
func (a *AutonomousState) waitIfPaused(ctx context.Context) error {
	a.mu.Lock()
	if !a.paused {
		a.mu.Unlock()
		return nil
	}
	resumeCh := a.resumeCh
	a.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-resumeCh:
		return nil
	}
}

// RunAutonomous starts the autonomous loop. Each round is a real Event
// in the Store — the loop prompts the agent with a continuation directive
// ("continue"), which flows through SendMessage exactly like a user turn:
// stored as a user message in the corpus, enters Local Context for RRC, drives
// Selection. The autonomous intent lives at THIS layer; downstream
// assembly and adapters stay pure (no prompt augmentation). Attachments
// supplied here land on the initial kickoff message only.
//
// Per spec (docs/spec/web/MANIFEST.adoc:187): "Autonomous run fails mid-execution
// → agent pauses with error visible, user reviews, retries or stops."
// A SendMessage error is NOT an exit condition — the loop pauses via
// autoState, lets OnAutonomousError surface the error (published to the
// UI + written into the thread as a system message), and waits. The
// user decides: resume (with optional correction) or stop. This matches
// how the same class of failure is handled in normal (non-autonomous)
// sends — spec calls for symmetric error handling.
func (r *Runner) RunAutonomous(ctx context.Context, prompt string, duration time.Duration, attachments ...*pb.AttachmentContent) error {
	r.mu.Lock()
	r.autoState = newAutonomousState()
	r.mu.Unlock()

	if _, err := r.SendMessage(ctx, prompt, pb.SelectionScope_SELECTION_SCOPE_THREAD, attachments...); err != nil {
		// Kickoff error: if a handler is wired, pause and surface; otherwise
		// the loop can't start so return as before.
		if r.OnAutonomousError != nil {
			r.OnAutonomousError(err)
		} else {
			return err
		}
	}

	started := time.Now()
	// Seed round from persisted state so resume / reconcile continue
	// the count instead of restarting it. StartAutonomous already
	// reset round_count=0 via StartAutonomousRun for fresh runs;
	// ResumeAgent and the boot reconciler preserve the prior count.
	// Without this seed, every re-entry into RunAutonomous reset a
	// local counter to 1 and the first stored value was always 2,
	// hiding hundreds of ticks behind a static round number.
	round := 0
	if st, _ := r.db.GetAgentState(r.threadID); st != nil {
		round = st.RoundCount
	}
	deadline := time.After(duration)
	for {
		// Check pause — blocks here if an error paused us last iteration.
		if err := r.autoState.waitIfPaused(ctx); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return nil
		default:
			// Autonomous tick is an empty Event: runner passes
			// msg=nil to ADK which walks session history.
			//
			// Per spec §142 the timer is the only natural exit; the
			// model is not allowed to signal completion. BUT a turn
			// where the model returns zero thinking + zero tool_call
			// + zero content IS a failure (ADK wiring dropped events,
			// stream parser broke, or the model genuinely refused).
			// That's distinct from RRC's "zero-return valid" axiom
			// which is about Selection returning nothing, not about
			// model output. Surface it via OnAutonomousError — the
			// loop pauses, the operator reviews, and resumes with a
			// correction if warranted. The timer still governs the
			// outer envelope; per-turn failures can still interrupt
			// for operator visibility.
			// Tag the trace layer with the round this tick BELONGS to
			// — the in-flight round, equal to (last completed round + 1).
			// Without this the trace's defer reads agent_state.round_count
			// before the loop increments it, producing an off-by-one in
			// every autonomous trace row.
			r.SetTickRound(round + 1)
			if _, err := r.SendMessage(ctx, "", pb.SelectionScope_SELECTION_SCOPE_THREAD); err != nil {
				if errors.Is(err, ErrNoResponse) {
					// Per ErrNoResponse's contract (runner.go:455-461):
					// in autonomous mode a zero-events turn is a no-op
					// continuation, not a pause condition. The model
					// having nothing to add on a given tick is not a
					// failure. Log + tick + loop. Don't fire
					// OnAutonomousError — that handler pauses the loop,
					// which would violate the documented contract.
					log.Printf("[Autonomous] tick produced no events on %s — continuing (ErrNoResponse is a no-op in autonomous mode)", r.threadID)
				} else if r.OnAutonomousError != nil {
					r.OnAutonomousError(err)
					continue
				} else {
					return err
				}
			}
			round++
			if r.onRound != nil {
				r.onRound(round, time.Since(started))
			}
		}
	}
}

// IsAutonomousActive reports whether an autonomous loop goroutine is live
// on this runner. Used by resumeAgent to distinguish "unpause the active
// loop" from "restart a dead loop" — the UX of "resume" should not silently
// no-op when the underlying goroutine has exited.
func (r *Runner) IsAutonomousActive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.autoState != nil
}

// PauseAutonomous pauses the autonomous loop.
func (r *Runner) PauseAutonomous() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.autoState != nil {
		r.autoState.Pause()
	}
}

// ResumeAutonomous resumes the autonomous loop.
func (r *Runner) ResumeAutonomous() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.autoState != nil {
		r.autoState.Resume()
	}
}
