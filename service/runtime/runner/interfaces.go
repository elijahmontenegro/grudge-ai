package runner

import (
	"github.com/emontenegr/spidey/core/retry"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// Pubsub fans out runtime events to UI subscribers. The factory
// emits; it never reads back. Implementations translate runtime
// structs into protocol-specific payloads — graph.Resolver maps
// into gqlgen-generated types, a future REST gateway would marshal
// to JSON, an in-memory test stub appends to a slice.
//
// PublishRetry takes the raw retry.Event so the implementation can
// enrich the event with sibling state (DB-stored mode/round/etc.)
// without coupling the runtime layer to those sources.
type Pubsub interface {
	PublishStream(StreamDelta)
	PublishAgentState(AgentStateUpdate)
	PublishToolExec(ToolExec)
	PublishSubagent(SubagentEvent)
	PublishRetry(threadID string, ev retry.Event)
}

// Approvals registers and resolves pending tool-approval and
// AskUserQuestion answer channels. Each Register call returns a
// receive channel and a paired unregister thunk; callers defer the
// unregister to clean up the registration table.
type Approvals interface {
	RegisterApproval(callID, threadID string) (<-chan bool, func())
	RegisterAnswer(callID string) (<-chan string, func())
}

// PlanStore is the per-thread plan-content cache. ExitPlan sets
// content; agent-state publishes read it back to attach to the
// outgoing AgentStateUpdate.
type PlanStore interface {
	GetPlan(threadID string) string
	SetPlan(threadID, content string)
}

// Selections records one selection-event turn in a single call —
// in-memory citation tally plus durable DB persistence. The
// implementation owns whatever indexes it needs to answer
// SelectionResult queries.
type Selections interface {
	RecordSelection(threadID string, result *pb.SelectionResult)
}

// EmbedEnqueuer enqueues a message ID for post-insert chunk
// embedding against the search/RRC vector index. Hot path; the
// implementation is responsible for tolerating a torn-down queue
// silently (settings reload may have cleared it).
type EmbedEnqueuer interface {
	Enqueue(messageID string)
}
