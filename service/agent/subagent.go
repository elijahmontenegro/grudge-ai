package agent

import (
	"context"
	"fmt"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
)

// SpawnSubagent creates an ephemeral thread fork for a subtask.
func (r *Runner) SpawnSubagent(ctx context.Context, task string, forkThreadID string) (*Runner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	forkedEngine, err := r.engine.Fork(r.threadID)
	if err != nil {
		return nil, fmt.Errorf("fork engine: %w", err)
	}

	fork, err := NewRunner(forkedEngine, r.completer, r.db, forkThreadID, r.tools, "")
	if err != nil {
		return nil, fmt.Errorf("create fork runner: %w", err)
	}

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

// EngineRef returns the runner's engine for external fork/merge.
func (r *Runner) EngineRef() *rrc.Engine {
	return r.engine
}
