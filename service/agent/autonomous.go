package agent

import (
	"context"
	"time"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// RunAutonomous starts the autonomous loop. The model continues receiving
// RRC rounds with empty user messages until the timer expires or context
// is cancelled. No continuation prompt. The selection IS the context.
func (r *Runner) RunAutonomous(ctx context.Context, prompt string, duration time.Duration) error {
	r.SetMode(ModeAutonomous)
	defer r.SetMode(ModeNormal)

	// First round: user's prompt
	if _, err := r.SendMessage(ctx, prompt, pb.SelectionScope_SELECTION_SCOPE_THREAD); err != nil {
		return err
	}

	deadline := time.After(duration)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return nil
		default:
			// Continue with empty user message — the selection drives the model
			if _, err := r.SendMessage(ctx, "", pb.SelectionScope_SELECTION_SCOPE_THREAD); err != nil {
				return err
			}
		}
	}
}
