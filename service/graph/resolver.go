package graph

import (
	"context"
	"fmt"
	"log"

	"github.com/emontenegr/spidey/core/httpc/retry"
	pb "github.com/emontenegr/spidey/proto/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/service/agent"
	"github.com/emontenegr/spidey/service/approvals"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/hooks"
	"github.com/emontenegr/spidey/service/messages"
	"github.com/emontenegr/spidey/service/plans"
	"github.com/emontenegr/spidey/service/prompt"
	"github.com/emontenegr/spidey/service/pubsub"
	"github.com/emontenegr/spidey/service/runtime"
	"github.com/emontenegr/spidey/service/selections"
	"github.com/emontenegr/spidey/service/skills"
	"github.com/emontenegr/spidey/service/storage"
	"github.com/emontenegr/spidey/service/substrate"

	"google.golang.org/adk/tool"
)

// Resolver is the GraphQL root resolver. Holds explicit references
// to every long-lived service component the resolvers reach into.
// Each field has a single, named owner (no god-struct embed): the
// substrate Holder owns the engine + embed-queue atomic swap; plans
// / selections / approvals each own one in-memory cache; runners
// owns the per-thread runner registry; the inserter centralizes
// message-store + chunk derivation + embed enqueue.
//
// Pubsub topics are graph-specific (gqlgen-generated event structs);
// they stay on the resolver. The runtime layer emits plain Go structs
// and the deps.go bridge translates.
type Resolver struct {
	cfg        *config.Config
	db         *storage.DB
	substrate  *substrate.Holder
	runners    *runtime.Registry
	plans      *plans.Cache
	selections *selections.Tracker
	approvals  *approvals.Registry
	inserter   *messages.Inserter
	skills     []skills.Skill
	mcpTools   []tool.Tool
	assembler  *prompt.Assembler
	hooks      *hooks.Dispatcher

	// Per-thread fan-out topics for UI subscriptions. Each is a
	// thin instance of pubsub.Topic / pubsub.Broadcast.
	streams   *pubsub.Topic[*StreamEvent]
	agents    *pubsub.Topic[*AgentState]
	tools *pubsub.Topic[*ToolExecution]
	subagents *pubsub.Topic[*SubagentProgress]
	threads   *pubsub.Broadcast[*ThreadStateEvent]
}

// Deps bundles the application-wide handles the resolver needs at
// construction. main.go composes everything explicitly and hands it
// here; the resolver doesn't own provider lifecycle or storage
// open/close.
type Deps struct {
	Config     *config.Config
	DB         *storage.DB
	Substrate  *substrate.Holder
	Runners    *runtime.Registry
	Plans      *plans.Cache
	Selections *selections.Tracker
	Approvals  *approvals.Registry
	Inserter   *messages.Inserter
	Skills     []skills.Skill
	MCPTools   []tool.Tool
	Assembler  *prompt.Assembler
	Hooks      *hooks.Dispatcher
}

// NewResolver wires the GraphQL resolver from the application's
// explicit deps. Every shared concern flows in through Deps —
// there's no embed, no god struct, no implicit promotion.
func NewResolver(d Deps) *Resolver {
	return &Resolver{
		cfg:        d.Config,
		db:         d.DB,
		substrate:  d.Substrate,
		runners:    d.Runners,
		plans:      d.Plans,
		selections: d.Selections,
		approvals:  d.Approvals,
		inserter:   d.Inserter,
		skills:     d.Skills,
		mcpTools:   d.MCPTools,
		assembler:  d.Assembler,
		hooks:      d.Hooks,

		streams:   pubsub.NewTopic[*StreamEvent](),
		agents:    pubsub.NewTopic[*AgentState](),
		tools: pubsub.NewTopic[*ToolExecution](),
		subagents: pubsub.NewTopic[*SubagentProgress](),
		threads:   pubsub.NewBroadcast[*ThreadStateEvent](),
	}
}

// getOrCreateRunner returns the active runner for a thread, creating
// one via the runtime factory if no entry exists. GetOrBuild covers
// the lookup-then-construct dance under one critical section so two
// simultaneous callers for the same thread (two browser tabs, boot
// reconciler racing first user message) don't both run the build
// closure and orphan the loser's runner.
func (r *Resolver) getOrCreateRunner(threadID string) (*agent.Runner, error) {
	entry, err := r.runners.GetOrBuild(threadID, func() (*runtime.Entry, error) {
		return runtime.Build(threadID, r.runtimeDeps())
	})
	if err != nil {
		return nil, fmt.Errorf("build runner %s: %w", threadID, err)
	}
	return entry.Runner, nil
}

// storeMessage routes through the inserter — chunk derivation,
// InsertMessage, and embed enqueue all happen in one place
// (service/messages). Graph-side and runtime-side inserts converge
// on the same code path.
func (r *Resolver) storeMessage(msg *pb.Message, _text string) error {
	return r.inserter.Insert(msg)
}

// stopRunner stops and removes a thread's runner. The actual
// teardown (subagent merge + publish, turn cancel, ctx cancel,
// delete) lives on runtime.Registry; this shim wraps it with the
// graph-typed Pubsub adapter so schema.resolvers callsites stay
// terse.
func (r *Resolver) stopRunner(threadID string) {
	r.runners.Stop(threadID, runtimePubsub{r})
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

	st, _ := r.db.GetAgentState(threadID)
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

