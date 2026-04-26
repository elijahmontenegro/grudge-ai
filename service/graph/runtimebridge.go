package graph

import (
	"log"

	"github.com/emontenegr/spidey/core/retry"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	runtimerunner "github.com/emontenegr/spidey/service/runtime/runner"
)

// runtimeDeps assembles the runtime/runner.Deps bundle for the
// factory. Cheap to recompute per call — every interface field is
// a zero-state shim over *Resolver.
func (r *Resolver) runtimeDeps() runtimerunner.Deps {
	return runtimerunner.Deps{
		Registry:      r.runners,
		DB:            r.DB,
		Engine:        r.Engine,
		Config:        r.Config,
		Main:          r.Main,
		Assembler:     r.Assembler,
		Hooks:         r.Hooks,
		Skills:        r.Skills,
		MCPTools:      r.MCPTools,
		Pubsub:        runtimePubsub{r},
		Approvals:     runtimeApprovals{r},
		PlanStore:     runtimePlanStore{r},
		Selections:    runtimeSelections{r},
		EmbedEnqueuer: runtimeEmbed{r},
	}
}

// runtimePubsub adapts the Resolver's per-shape pubsub topics to
// the runtime/runner.Pubsub interface. Translates plain runtime
// structs into gqlgen-generated graph types so the runtime layer
// stays free of GraphQL-specific symbols.
type runtimePubsub struct{ r *Resolver }

func (a runtimePubsub) PublishStream(ev runtimerunner.StreamDelta) {
	out := &StreamEvent{MessageID: ev.MessageID, Done: ev.Done}
	if ev.Delta != "" {
		d := ev.Delta
		out.Delta = &d
	}
	if ev.Thinking != "" {
		t := ev.Thinking
		out.Thinking = &t
	}
	a.r.streams.Publish(ev.ThreadID, out)
}

func (a runtimePubsub) PublishAgentState(ev runtimerunner.AgentStateUpdate) {
	out := &AgentState{
		ThreadID:   ev.ThreadID,
		Status:     toGQLStatus(ev.Status),
		Mode:       toGQLMode(ev.Mode),
		RoundCount: ev.RoundCount,
	}
	if ev.ElapsedTime != "" {
		e := ev.ElapsedTime
		out.ElapsedTime = &e
	}
	if ev.DurationLimit != "" {
		d := ev.DurationLimit
		out.DurationLimit = &d
	}
	if ev.PlanContent != "" {
		p := ev.PlanContent
		out.PlanContent = &p
	}
	if ev.Retry != nil {
		rs := &RetryStatus{
			Attempt:     ev.Retry.Attempt,
			MaxAttempts: ev.Retry.MaxAttempts,
			NextDelayMs: ev.Retry.NextDelayMs,
			Final:       ev.Retry.Final,
		}
		if ev.Retry.Error != "" {
			e := ev.Retry.Error
			rs.Error = &e
		}
		out.Retry = rs
	}
	a.r.agents.Publish(ev.ThreadID, out)
}

func (a runtimePubsub) PublishToolExec(ev runtimerunner.ToolExec) {
	out := &ToolExecution{
		ThreadID:  ev.ThreadID,
		CallID:    ev.CallID,
		ToolName:  ev.ToolName,
		Arguments: ev.Arguments,
		Status:    ev.Status,
	}
	// Result is meaningful only for terminal-with-payload statuses;
	// others (pending/running/waiting_for_user/etc.) carry no result.
	if ev.Status == "completed" || ev.Status == "failed" {
		r := ev.Result
		out.Result = &r
		ie := ev.IsError
		out.IsError = &ie
	}
	a.r.tools.Publish(ev.ThreadID, out)
}

func (a runtimePubsub) PublishSubagent(ev runtimerunner.SubagentEvent) {
	a.r.subagents.Publish(ev.ThreadID, &SubagentProgress{
		ThreadID:     ev.ThreadID,
		ForkThreadID: ev.ForkThreadID,
		Task:         ev.Task,
		Status:       ev.Status,
		RoundCount:   ev.RoundCount,
	})
}

// PublishRetry routes through the resolver's existing
// publishRetryStatus, which reads sibling state from the DB to
// fill in non-retry fields so a retry indicator doesn't blank the
// UI's view of mode/round_count.
func (a runtimePubsub) PublishRetry(threadID string, ev retry.Event) {
	a.r.publishRetryStatus(threadID, ev)
}

func toGQLStatus(s runtimerunner.AgentStatus) AgentStatus {
	switch s {
	case runtimerunner.AgentStatusRunning:
		return AgentStatusRunning
	case runtimerunner.AgentStatusPaused:
		return AgentStatusPaused
	default:
		return AgentStatusIdle
	}
}

func toGQLMode(m runtimerunner.AgentMode) AgentMode {
	switch m {
	case runtimerunner.AgentModeAutonomous:
		return AgentModeAutonomous
	case runtimerunner.AgentModePlan:
		return AgentModePlan
	default:
		return AgentModeNormal
	}
}

// runtimeApprovals adapts the Resolver's pendingApprovals /
// pendingAnswers maps to the runtime/runner.Approvals interface.
// Each Register call returns a receive channel and a paired
// unregister thunk; the factory defers the unregister.
type runtimeApprovals struct{ r *Resolver }

func (a runtimeApprovals) RegisterApproval(callID, threadID string) (<-chan bool, func()) {
	ch := make(chan bool, 1)
	a.r.pendingApprovalsMu.Lock()
	a.r.pendingApprovals[callID] = ch
	a.r.pendingThreadIDs[callID] = threadID
	a.r.pendingApprovalsMu.Unlock()
	return ch, func() {
		a.r.pendingApprovalsMu.Lock()
		delete(a.r.pendingApprovals, callID)
		delete(a.r.pendingThreadIDs, callID)
		a.r.pendingApprovalsMu.Unlock()
	}
}

func (a runtimeApprovals) RegisterAnswer(callID string) (<-chan string, func()) {
	ch := make(chan string, 1)
	a.r.pendingApprovalsMu.Lock()
	a.r.pendingAnswers[callID] = ch
	a.r.pendingApprovalsMu.Unlock()
	return ch, func() {
		a.r.pendingApprovalsMu.Lock()
		delete(a.r.pendingAnswers, callID)
		a.r.pendingApprovalsMu.Unlock()
	}
}

// runtimePlanStore adapts the Resolver's per-thread planContent
// map to the runtime/runner.PlanStore interface. ApprovePlan /
// RejectPlan continue to access the underlying map directly —
// the factory only needs Get/Set.
type runtimePlanStore struct{ r *Resolver }

func (a runtimePlanStore) GetPlan(threadID string) string {
	a.r.planContentMu.RLock()
	defer a.r.planContentMu.RUnlock()
	return a.r.planContent[threadID]
}

func (a runtimePlanStore) SetPlan(threadID, content string) {
	a.r.planContentMu.Lock()
	a.r.planContent[threadID] = content
	a.r.planContentMu.Unlock()
}

// runtimeSelections records a selection event in the resolver's
// in-memory citation map and persists via DB. The map is the hot
// read path for queryResolver.SelectionResult; persistence is the
// durable record that survives restart for historical audit.
type runtimeSelections struct{ r *Resolver }

func (a runtimeSelections) RecordSelection(threadID string, result *pb.SelectionResult) {
	a.r.mu.Lock()
	a.r.selectionResults[result.EventId] = result
	a.r.latestSelection[threadID] = result.EventId
	for _, sel := range result.Selected {
		a.r.citationCount[sel.MessageId]++
	}
	a.r.mu.Unlock()

	// Event IDs are synthesized as sel-<target_message_id> in the
	// engine. Strip the prefix to recover the target for the
	// selections table FK.
	targetID := result.EventId
	if len(targetID) > 4 && targetID[:4] == "sel-" {
		targetID = targetID[4:]
	}
	if err := a.r.DB.SaveSelection(result, targetID, threadID); err != nil {
		log.Printf("SaveSelection(event=%s target=%s): %v", result.EventId, targetID, err)
	}
}

// runtimeEmbed delegates to the resolver's atomic-pointer-managed
// EmbedQueue. Tolerates a nil queue (settings reload may have
// torn it down) — silent no-op falls back to the startup
// backfill goroutine and the live-embed path in
// ChunkOracle.EnsureVector.
type runtimeEmbed struct{ r *Resolver }

func (a runtimeEmbed) Enqueue(msgID string) {
	if q := a.r.EmbedQueue(); q != nil {
		q.Enqueue(msgID)
	}
}
