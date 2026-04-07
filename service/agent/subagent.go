package agent

import (
	"context"
	"fmt"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
)

// SpawnSubagent creates an ephemeral thread fork for a subtask.
// Returns a Runner for the forked thread.
func (r *Runner) SpawnSubagent(ctx context.Context, task string, forkThreadID string) (*Runner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	forkedEngine, err := r.engine.Fork(r.threadID)
	if err != nil {
		return nil, fmt.Errorf("fork engine: %w", err)
	}

	fork := NewRunner(forkedEngine, r.completer, r.db, forkThreadID)

	// Send the task as the first message in the fork
	if _, err := fork.SendMessage(ctx, task, pb.SelectionScope_SELECTION_SCOPE_THREAD); err != nil {
		return nil, fmt.Errorf("subagent first message: %w", err)
	}

	return fork, nil
}

// MergeSubagent merges a fork's edges and scores back into the parent.
func (r *Runner) MergeSubagent(fork *Runner) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.engine.Merge(fork.engine, r.threadID)
}

// Engine exposes the runner's engine for fork/merge operations.
func (r *Runner) Engine() *rrc.Engine {
	return r.engine
}
