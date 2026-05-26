package agent

import (
	"context"
	"fmt"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// SpawnSubagent creates an ephemeral thread fork for a subtask.
//
// Must NOT acquire r.mu: SpawnSubagent is invoked from the Agent tool during
// the parent runner's SendMessage, which already holds r.mu. sync.Mutex is
// not reentrant, so re-locking would deadlock the thread. The accessed
// fields (engine, threadID, completer, db, tools, modelName, instruction)
// are either immutable after NewRunner or have their own synchronization
// (engine owns its own lock; db internally).
func (r *Runner) SpawnSubagent(ctx context.Context, task string, forkThreadID string) (*Runner, error) {
	forkedEngine := r.engine.Fork()

	fork, err := NewRunner(forkedEngine, r.completer, r.db, forkThreadID, r.tools, r.modelName, r.instruction, r.rerankerModelID)
	if err != nil {
		return nil, fmt.Errorf("create fork runner: %w", err)
	}

	if _, err := fork.SendMessage(ctx, task, pb.SelectionScope_SELECTION_SCOPE_THREAD); err != nil {
		return nil, fmt.Errorf("subagent first message: %w", err)
	}

	return fork, nil
}

// MergeSubagent merges a fork's edges and scores back into the parent.
// Same no-r.mu rule as SpawnSubagent — called from Agent tool context.
// r.engine.Merge uses the engine's own lock internally.
func (r *Runner) MergeSubagent(fork *Runner) error {
	return r.engine.Merge(fork.engine)
}

