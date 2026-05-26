package tools

import "context"

// Tool capability interfaces. Each tool depends only on the
// capabilities it uses (interface segregation); the runtime layer
// supplies one concrete value (*runtime.toolAgent) that satisfies
// all of them structurally.
//
// Adding a new capability adds a method to the appropriate
// interface; the compiler flags every implementer until they catch up.

// SubAgentOps fork/route operations against subagent threads.
type SubAgentOps interface {
	// SpawnAgent forks a subagent runner under forkID and runs
	// the given task autonomously to completion. Returns a
	// human-readable summary (the spawn confirmation message
	// shown to the parent's model).
	SpawnAgent(ctx context.Context, task, forkID string) (string, error)

	// SendToAgent sends a user-message into a running subagent's
	// thread. Returns a brief summary of the subagent's response.
	SendToAgent(ctx context.Context, agentID, message string) (string, error)
}

// UserAsker prompts the user via the UI and waits for an answer.
type UserAsker interface {
	// AskUser prompts the user with one or more multiple-choice
	// questions and blocks until they answer or the request times
	// out. Returns a map of question → answer; missing or
	// timed-out questions map to empty strings (the model sees a
	// complete map so it doesn't loop on the same call).
	AskUser(ctx context.Context, args AskUserArgs) (map[string]string, error)
}

// ModeOps transitions and inspects the per-thread agent mode.
type ModeOps interface {
	// SetMode transitions the per-thread agent state mode (e.g.,
	// "plan", "autonomous", "normal"). The pubsub broadcast and
	// DB update happen behind the contract.
	SetMode(ctx context.Context, mode string) error

	// IsPlanMode reports whether the agent is currently in plan
	// mode. Used by the plan-guard that blocks write tools.
	IsPlanMode() bool

	// OnPlanContent publishes the ExitPlanMode tool's compiled
	// plan content to the frontend (plan store + pubsub).
	OnPlanContent(content string)
}

// Approver gates a tool call against user approval when the
// permission policy requires it.
type Approver interface {
	// RequireApproval blocks for user approval on a tool call
	// that the permission policy gates as "ask". The returned
	// bool is true iff the user approved. Errors include context
	// cancellation, approval timeout, and the user-denial case
	// (returned as a typed error so the tool surfaces it to the
	// model).
	RequireApproval(ctx context.Context, callID, toolName, argsJSON string) (bool, error)
}

// HookFirer runs configured pre/post lifecycle hooks.
type HookFirer interface {
	// FireHook runs any configured user hooks for the given
	// (event, tool) pair. Returns an error if a hook blocks the
	// tool call.
	FireHook(ctx context.Context, event, toolName string) error
}
