package graph

import (
	"github.com/elijahmontenegro/grudge/core/httpc/retry"
	"github.com/elijahmontenegro/grudge/service/runtime"
)

// runtimeDeps assembles the runtime.Deps bundle for the runner
// factory. Each field maps directly to a resolver-held dependency;
// the substrate Holder owns the engine/main-completer atomic-swap
// reads. Pubsub is the only adapter type — it translates plain
// runtime event structs into gqlgen-generated graph types so the
// runtime layer stays free of GraphQL symbols.
func (r *Resolver) runtimeDeps() runtime.Deps {
	return runtime.Deps{
		Registry:      r.runners,
		DB:            r.db,
		Engine:        r.substrate.Engine(),
		Config:        r.cfg,
		Main:          r.substrate.Main(),
		Assembler:     r.assembler,
		Hooks:         r.hooks,
		Skills:        r.skills,
		MCPTools:      r.mcpTools,
		Inserter:      r.inserter,
		Pubsub:        runtimePubsub{r},
		Approvals:     r.approvals,
		PlanStore:     r.plans,
		Selections:    r.selections,
		EmbedEnqueuer: r.substrate,
	}
}

// runtimePubsub adapts the Resolver's per-shape pubsub topics to
// the runtime/runner.Pubsub interface. Translates plain runtime
// structs into gqlgen-generated graph types so the runtime layer
// stays free of GraphQL-specific symbols.
type runtimePubsub struct{ r *Resolver }

func (a runtimePubsub) PublishStream(ev runtime.StreamDelta) {
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

func (a runtimePubsub) PublishAgentState(ev runtime.AgentStateUpdate) {
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

func (a runtimePubsub) PublishToolExec(ev runtime.ToolExec) {
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

func (a runtimePubsub) PublishSubagent(ev runtime.SubagentEvent) {
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

func toGQLStatus(s runtime.AgentStatus) AgentStatus {
	switch s {
	case runtime.AgentStatusRunning:
		return AgentStatusRunning
	case runtime.AgentStatusPaused:
		return AgentStatusPaused
	default:
		return AgentStatusIdle
	}
}

func toGQLMode(m runtime.AgentMode) AgentMode {
	switch m {
	case runtime.AgentModeAutonomous:
		return AgentModeAutonomous
	case runtime.AgentModePlan:
		return AgentModePlan
	default:
		return AgentModeNormal
	}
}
