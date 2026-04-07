package agent

import (
	"context"
	"time"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// RunAutonomous starts the autonomous loop. The model continues receiving
// RRC rounds with empty user messages until the timer expires or context
// is cancelled. No continuation prompt — the selection drives the model.
func (r *Runner) RunAutonomous(ctx context.Context, prompt string, duration time.Duration) error {
	r.SetMode(ModeAutonomous)
	defer r.SetMode(ModeNormal)

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
			if _, err := r.SendMessage(ctx, "", pb.SelectionScope_SELECTION_SCOPE_THREAD); err != nil {
				return err
			}
		}
	}
}
