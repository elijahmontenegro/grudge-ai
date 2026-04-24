package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
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
// stored as a user message in the corpus, becomes the Query for RRC, drives
// Selection. The autonomous intent lives at THIS layer; downstream
// assembly and adapters stay pure (no prompt augmentation). Attachments
// supplied here land on the initial kickoff message only.
//
// Per spec (spec/web/MANIFEST.adoc:187): "Autonomous run fails mid-execution
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

	if _, err := r.SendMessage(ctx, prompt, pb.SelectionScope_SELECTION_SCOPE_THREAD, attachments...); err != nil && !errors.Is(err, ErrNoResponse) {
		// Kickoff error (genuine, not ErrNoResponse): if a handler is
		// wired, pause and surface; otherwise the loop can't start so
		// return as before. ErrNoResponse on kickoff (empty response
		// to the kickoff prompt) isn't a failure — loop proceeds to
		// ticking.
		if r.OnAutonomousError != nil {
			r.OnAutonomousError(err)
		} else {
			return err
		}
	}

	started := time.Now()
	round := 1
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
			// msg=nil to ADK which walks session history. If the
			// model has nothing to add, ADK yields zero events and
			// processEvents returns ErrNoResponse.
			//
			// Per spec (MANIFEST.adoc §142): the timer is the only
			// natural exit. The model is NOT allowed to signal
			// completion of an autonomous run — a silent tick is a
			// no-op, not a pause-trigger. The loop ticks through
			// until the deadline regardless of whether any
			// individual round produced output.
			//
			// Real errors (provider unreachable, classifier offline,
			// protocol validation failure) still pause via
			// OnAutonomousError so the operator can intervene.
			_, err := r.SendMessage(ctx, "", pb.SelectionScope_SELECTION_SCOPE_THREAD)
			if err != nil && !errors.Is(err, ErrNoResponse) {
				if r.OnAutonomousError != nil {
					r.OnAutonomousError(err)
					continue
				}
				return err
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
