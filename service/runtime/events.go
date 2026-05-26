package runtime

// Plain Go enums for status and mode. The runtime layer publishes
// these on its event channel; consumers (graph resolver, future REST
// gateway) translate to whatever protocol-specific representation
// they expose. Keeping the runtime layer free of gqlgen-generated
// types is what lets it be reused by a non-GraphQL consumer.

type AgentStatus int

const (
	AgentStatusIdle AgentStatus = iota
	AgentStatusRunning
	AgentStatusPaused
)

type AgentMode int

const (
	AgentModeNormal AgentMode = iota
	AgentModeAutonomous
	AgentModePlan
)

// StreamDelta carries one streaming chunk surfaced from a runner.
// Done terminates the stream; intermediate emissions carry Delta
// and/or Thinking text.
type StreamDelta struct {
	ThreadID  string
	MessageID string
	Delta     string
	Thinking  string
	Done      bool
}

// AgentStateUpdate is one state-publish event for a thread. Empty
// strings on optional fields signal absent — the consumer-side
// adapter translates empty to whatever its protocol expects (nil
// pointer for gqlgen, omitted JSON field for REST, etc.).
type AgentStateUpdate struct {
	ThreadID      string
	Status        AgentStatus
	Mode          AgentMode
	RoundCount    int
	ElapsedTime   string
	DurationLimit string
	PlanContent   string
	Retry         *RetryStatus
}

// RetryStatus carries one retry-event progression for surfacing in
// the UI's retry indicator. Final+empty Error = success clear;
// Final+non-empty Error = terminal failure.
type RetryStatus struct {
	Attempt     int
	MaxAttempts int
	NextDelayMs int
	Final       bool
	Error       string
}

// ToolExec is one tool-call lifecycle event. Status is one of:
// pending / running / completed / failed / approved / denied /
// cancelled / timeout / waiting_for_user.
type ToolExec struct {
	ThreadID  string
	CallID    string
	ToolName  string
	Arguments string
	Status    string
	Result    string
	IsError   bool
}

// SubagentEvent is one progress update from a forked subagent
// thread. Status is "running" or "completed".
type SubagentEvent struct {
	ThreadID     string
	ForkThreadID string
	Task         string
	Status       string
	RoundCount   int
}
