package tools

import "context"

// Agent is the runner-side contract every tool depends on.
//
// Replaces the prior closure-of-callbacks pattern (a struct of
// `func(...) error` fields the caller had to populate by hand).
// That pattern enforced the dependency direction — tools called
// the closures, no import of the runner package — but had a real
// failure mode: a tool calling a nil closure crashed at runtime,
// at the wrong time, in the wrong stack frame. An interface makes
// "satisfaction" a compile-time property: a type either implements
// every method or the BuildTools call fails to compile.
//
// The runtime wires a concrete agent (service/runtime constructs
// one per-thread, closing over kernel facilities: DB, Pubsub,
// Registry, Hooks, PlanStore) and passes it through Deps. Methods
// take ctx where the operation is async or cancellable; pure
// state-reads (IsPlanMode, OnPlanContent) don't.
//
// Adding a new tool capability adds a method here, the compiler
// flags every implementer until they catch up.
type Agent interface {
	// SpawnAgent forks a subagent runner under forkID and runs
	// the given task autonomously to completion. Returns a
	// human-readable summary (the spawn confirmation message
	// shown to the parent's model).
	SpawnAgent(ctx context.Context, task, forkID string) (string, error)

	// SendToAgent sends a user-message into a running subagent's
	// thread. Returns a brief summary of the subagent's response.
	SendToAgent(ctx context.Context, agentID, message string) (string, error)

	// AskUser prompts the user with one or more multiple-choice
	// questions and blocks until they answer or the request
	// times out. Returns a map of question → answer; missing or
	// timed-out questions map to empty strings (the model sees a
	// complete map so it doesn't loop on the same call).
	AskUser(ctx context.Context, args AskUserArgs) (map[string]string, error)

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

	// RequireApproval blocks for user approval on a tool call
	// that the permission policy gates as "ask". The returned
	// bool is true iff the user approved. Errors include context
	// cancellation, approval timeout, and the user-denial case
	// (returned as a typed error so the tool surfaces it to the
	// model).
	RequireApproval(ctx context.Context, callID, toolName, argsJSON string) (bool, error)

	// FireHook runs any configured user hooks for the given
	// (event, tool) pair. Returns an error if a hook blocks the
	// tool call.
	FireHook(ctx context.Context, event, toolName string) error
}
