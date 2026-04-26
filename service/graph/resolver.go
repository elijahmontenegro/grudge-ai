package graph

import (
	"context"
	"fmt"
	"log"

	"github.com/emontenegr/spidey/core/retry"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/service/agent"
	"github.com/emontenegr/spidey/service/runtime/kernel"
	"github.com/emontenegr/spidey/service/runtime/pubsub"
	runtimerunner "github.com/emontenegr/spidey/service/runtime/runner"
	"github.com/emontenegr/spidey/service/storage"
)

// Resolver is the GraphQL root resolver. It embeds *kernel.Kernel
// for shared runtime state (DB, Engine, Config, Main, Runners,
// plan/selection caches, approval channels) and adds the GraphQL-
// specific surface: pubsub topics typed against gqlgen's event
// structs and the publish helpers that fan out to subscribers.
//
// Substrate, ReloadProviders, plan-content/selection/approval
// state, and the embed queue all live on Kernel. The graph layer
// reads them through promoted fields/methods.
type Resolver struct {
	*kernel.Kernel

	// Per-thread fan-out topics for UI subscriptions. Each is a
	// thin instance of pubsub.Topic / pubsub.Broadcast — the five
	// near-identical sub/pub maps that used to live here are now
	// one generic primitive parameterized per event shape.
	streams   *pubsub.Topic[*StreamEvent]
	agents    *pubsub.Topic[*AgentState]
	tools     *pubsub.Topic[*ToolExecution]
	subagents *pubsub.Topic[*SubagentProgress]
	threads   *pubsub.Broadcast[*ThreadStateEvent]
}

// NewResolver wraps a Kernel with the GraphQL fan-out surface.
// Every shared runtime concern (substrate, registry, caches,
// embed queue) flows in through the kernel; resolvers consume
// them via promotion.
func NewResolver(k *kernel.Kernel) *Resolver {
	return &Resolver{
		Kernel:    k,
		streams:   pubsub.NewTopic[*StreamEvent](),
		agents:    pubsub.NewTopic[*AgentState](),
		tools:     pubsub.NewTopic[*ToolExecution](),
		subagents: pubsub.NewTopic[*SubagentProgress](),
		threads:   pubsub.NewBroadcast[*ThreadStateEvent](),
	}
}

// getOrCreateRunner returns the active runner for a thread, creating
// one via the runtime/runner factory if no entry exists. The factory
// owns every concern — askCh goroutine, ToolDeps construction,
// prompt assembly, retry wrapping, callback wiring. The resolver
// supplies the Pubsub bridge (graph-typed translation) plus the
// kernel-implemented Approvals / PlanStore / Selections /
// EmbedEnqueuer interfaces via runtimeDeps.
func (r *Resolver) getOrCreateRunner(threadID string) (*agent.Runner, error) {
	if entry, ok := r.Runners.Get(threadID); ok {
		return entry.Runner, nil
	}
	entry, err := runtimerunner.Build(threadID, r.runtimeDeps())
	if err != nil {
		return nil, fmt.Errorf("build runner %s: %w", threadID, err)
	}
	r.Runners.Set(threadID, entry)
	return entry.Runner, nil
}

// storeMessage inserts a message (which cascades into chunk creation
// via the DB's chunker hook) and fires eager embedding of those
// chunks into the search/RRC cache. The embed is async — failure
// falls back to the startup backfill goroutine and the live-embed
// path in ChunkOracle.EnsureVector, so a slow or down embedder
// doesn't block a user mutation.
func (r *Resolver) storeMessage(msg *pb.Message, _text string) error {
	if err := r.DB.InsertMessage(msg); err != nil {
		return err
	}
	r.Enqueue(msg.Id)
	return nil
}

// stopRunner stops and removes a thread's runner.
// For subagent forks, merges edges and scores back into the parent before cleanup.
func (r *Resolver) stopRunner(threadID string) {
	entry, ok := r.Runners.Get(threadID)
	if !ok {
		return
	}
	// Merge subagent fork back into parent
	if entry.ParentThreadID != "" {
		if parentEntry, pOk := r.Runners.Get(entry.ParentThreadID); pOk {
			if err := parentEntry.Runner.MergeSubagent(entry.Runner); err != nil {
				log.Printf("Subagent merge %s → %s: %v", threadID, entry.ParentThreadID, err)
			}
		}
		r.publishSubagent(entry.ParentThreadID, &SubagentProgress{
			ThreadID: entry.ParentThreadID, ForkThreadID: threadID,
			Status: "completed",
		})
	}
	// Cancel the in-flight turn (if any) before cancelling the
	// autonomous-loop ctx. The turn's ctx is a child of whatever
	// caller ctx ADK is running under; entry.Cancel is the
	// autonomous outer loop's ctx. A non-autonomous streaming send
	// has no entry.Cancel — the turn-cancel is the only lever that
	// reaches it.
	if entry.Runner != nil {
		entry.Runner.CancelTurn()
	}
	if entry.Cancel != nil {
		entry.Cancel()
	}
	if entry.AskCh != nil {
		close(entry.AskCh)
	}
	r.Runners.Delete(threadID)
}

// unsubscribeOnDone waits for ctx.Done, then calls cleanup under the lock.
func unsubscribeOnDone(ctx context.Context, cleanup func()) {
	go func() {
		<-ctx.Done()
		cleanup()
	}()
}

// subscribe/publish thin delegates to the per-shape pubsub topics.
// Resolver methods stay in this package so the GraphQL resolver
// boilerplate doesn't need to know about pubsub.
func (r *Resolver) subscribeStream(threadID string) chan *StreamEvent {
	return r.streams.Subscribe(threadID)
}

func (r *Resolver) publishStream(threadID string, event *StreamEvent) {
	r.streams.Publish(threadID, event)
}

func (r *Resolver) subscribeAgentState(threadID string) chan *AgentState {
	return r.agents.Subscribe(threadID)
}

func (r *Resolver) publishAgentState(threadID string, state *AgentState) {
	r.agents.Publish(threadID, state)
}

// publishRetryStatus emits an AgentState with the retry field filled
// in, other fields pulled from the DB so a retry event doesn't wipe
// the frontend's view of status/mode/round_count. When ev.Final is
// true AND ev.Err is nil it's a success clear — publish with retry=nil
// so the UI hides the retry indicator.
func (r *Resolver) publishRetryStatus(threadID string, ev retry.Event) {
	// Log every retry event server-side so "why is this happening" has
	// an actual answer in spidey.log instead of being stuck in the UI's
	// retry subscription buffer. Final+no-error = success clear; Final
	// with an error = terminal; otherwise an in-flight retry with a
	// delay before the next attempt.
	switch {
	case ev.Final && ev.Err == nil:
		log.Printf("RETRY: thread=%s attempt=%d/%d cleared (success)", threadID, ev.Attempt, ev.MaxAttempts)
	case ev.Final && ev.Err != nil:
		log.Printf("RETRY: thread=%s attempt=%d/%d TERMINAL err=%q", threadID, ev.Attempt, ev.MaxAttempts, ev.Err.Error())
	default:
		log.Printf("RETRY: thread=%s attempt=%d/%d nextDelay=%s err=%q", threadID, ev.Attempt, ev.MaxAttempts, ev.NextDelay, ev.Err.Error())
	}

	st, _ := r.DB.GetAgentState(threadID)
	base := &AgentState{ThreadID: threadID}
	if st != nil {
		switch st.Status {
		case storage.AgentStatusRunning:
			base.Status = AgentStatusRunning
		case storage.AgentStatusPaused:
			base.Status = AgentStatusPaused
		default:
			base.Status = AgentStatusIdle
		}
		switch st.Mode {
		case storage.AgentModeAutonomous:
			base.Mode = AgentModeAutonomous
		case storage.AgentModePlan:
			base.Mode = AgentModePlan
		default:
			base.Mode = AgentModeNormal
		}
		base.RoundCount = st.RoundCount
		if st.DurationLimit != "" {
			d := st.DurationLimit
			base.DurationLimit = &d
		}
	}

	// Success clear: retry is done and succeeded. Frontend hides the indicator.
	if ev.Final && ev.Err == nil {
		r.publishAgentState(threadID, base)
		return
	}

	rs := &RetryStatus{
		Attempt:     ev.Attempt,
		MaxAttempts: ev.MaxAttempts,
		NextDelayMs: int(ev.NextDelay / 1e6),
		Final:       ev.Final,
	}
	if ev.Err != nil {
		msg := ev.Err.Error()
		rs.Error = &msg
	}
	base.Retry = rs
	r.publishAgentState(threadID, base)
}

func (r *Resolver) subscribeToolExec(threadID string) chan *ToolExecution {
	return r.tools.Subscribe(threadID)
}

func (r *Resolver) publishToolExec(threadID string, event *ToolExecution) {
	r.tools.Publish(threadID, event)
}

func (r *Resolver) subscribeSubagent(threadID string) chan *SubagentProgress {
	return r.subagents.Subscribe(threadID)
}

func (r *Resolver) publishSubagent(threadID string, event *SubagentProgress) {
	r.subagents.Publish(threadID, event)
}

func (r *Resolver) subscribeThreadState() chan *ThreadStateEvent {
	return r.threads.Subscribe()
}

func (r *Resolver) publishThreadState(event *ThreadStateEvent) {
	r.threads.Publish(event)
}
