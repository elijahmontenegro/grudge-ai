package agent

import (
	"context"
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

// RunAutonomous starts the autonomous loop. The model continues receiving
// RRC rounds with empty user messages until the timer expires, context
// is cancelled, or pause/resume controls are used.
func (r *Runner) RunAutonomous(ctx context.Context, prompt string, duration time.Duration) error {
	r.SetMode(ModeAutonomous)
	defer r.SetMode(ModeNormal)

	r.mu.Lock()
	r.autoState = newAutonomousState()
	r.mu.Unlock()

	if _, err := r.SendMessage(ctx, prompt, pb.SelectionScope_SELECTION_SCOPE_THREAD); err != nil {
		return err
	}

	deadline := time.After(duration)
	for {
		// Check pause
		if err := r.autoState.waitIfPaused(ctx); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return nil
		default:
			if _, err := r.SendMessage(ctx, "", pb.SelectionScope_SELECTION_SCOPE_THREAD); err != nil {
				return err
			}
		}
	}
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
