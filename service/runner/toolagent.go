package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/service/agent/tools"
	"github.com/emontenegr/spidey/service/hooks"
	"github.com/emontenegr/spidey/service/storage"
)

// toolAgent implements tools.Agent by closing over the kernel
// facilities (DB, Pubsub, Approvals, Registry, PlanStore, Hooks)
// plus the per-thread threadID. Each method translates a tool's
// abstract request into the concrete kernel call.
//
// Pre-Phase-9b this same logic lived as eight anonymous closures
// constructed in buildToolDeps. Promoting them to methods on a
// typed struct shifts contract-satisfaction from "did the caller
// remember to populate every closure field?" (runtime nil-deref)
// to "does *toolAgent satisfy tools.Agent?" (compile-time error).
type toolAgent struct {
	threadID  string
	db        *storage.DB
	pubsub    Pubsub
	approvals Approvals
	registry  *Registry
	planStore PlanStore
	hooks     *hooks.Dispatcher
}

// newToolAgent constructs a per-thread implementation of tools.Agent.
// The registry argument is the same kernel-wide registry every
// runner uses; SpawnAgent/SendToAgent look up sibling runners
// through it.
func newToolAgent(threadID string, deps Deps) *toolAgent {
	return &toolAgent{
		threadID:  threadID,
		db:        deps.DB,
		pubsub:    deps.Pubsub,
		approvals: deps.Approvals,
		registry:  deps.Registry,
		planStore: deps.PlanStore,
		hooks:     deps.Hooks,
	}
}

// SpawnAgent forks a subagent runner under forkID and runs the
// task. Parent's runner is reached via the kernel registry; the
// fork is registered with ParentThreadID set so MergeSubagent can
// be wired on stop.
func (a *toolAgent) SpawnAgent(ctx context.Context, task, forkID string) (string, error) {
	entry, ok := a.registry.Get(a.threadID)
	if !ok {
		return "", fmt.Errorf("no runner for thread %s", a.threadID)
	}
	fork, err := entry.Runner.SpawnSubagent(ctx, task, forkID)
	if err != nil {
		return "", err
	}
	a.registry.Set(forkID, &Entry{
		Runner:         fork,
		ParentThreadID: a.threadID,
	})
	a.pubsub.PublishSubagent(SubagentEvent{
		ThreadID: a.threadID, ForkThreadID: forkID,
		Task: task, Status: "running",
	})
	return "Subagent started: " + forkID, nil
}

// SendToAgent routes a message into a running subagent thread.
// After the subagent responds, edges and scores are merged back
// into the parent (idempotent — score writes are INSERT OR REPLACE).
func (a *toolAgent) SendToAgent(ctx context.Context, agentID, message string) (string, error) {
	entry, ok := a.registry.Get(agentID)
	if !ok {
		return "", fmt.Errorf("no runner for agent %s", agentID)
	}
	resp, err := entry.Runner.SendMessage(ctx, message, pb.SelectionScope_SELECTION_SCOPE_THREAD)
	if err != nil {
		return "", err
	}
	if entry.ParentThreadID != "" {
		if parentEntry, ok := a.registry.Get(entry.ParentThreadID); ok {
			parentEntry.Runner.MergeSubagent(entry.Runner)
		}
	}
	return fmt.Sprintf("Response from %s: %d blocks", agentID, len(resp.Content)), nil
}

// AskUser prompts the user via the UI and blocks for an answer or
// timeout. Replaces the prior channel+goroutine pattern (askCh +
// runAskLoop) — the goroutine added no concurrency the method
// shape doesn't already have, and the channel made cleanup-on-stop
// load-bearing (closing askCh terminated the loop). Method form:
// per-call lifecycle bounded by ctx; no orphan goroutines.
func (a *toolAgent) AskUser(ctx context.Context, args tools.AskUserArgs) (map[string]string, error) {
	callID := fmt.Sprintf("ask-%d", time.Now().UnixNano())
	argsJSON, err := json.Marshal(args)
	if err != nil {
		argsJSON = []byte(`{"questions":[]}`)
	}
	a.pubsub.PublishToolExec(ToolExec{
		ThreadID: a.threadID, CallID: callID, ToolName: "AskUserQuestion",
		Arguments: string(argsJSON), Status: "waiting_for_user",
	})
	respCh, unregister := a.approvals.RegisterAnswer(callID)
	defer unregister()
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	publishCompletion := func(status string) {
		a.pubsub.PublishToolExec(ToolExec{
			ThreadID: a.threadID, CallID: callID, ToolName: "AskUserQuestion",
			Arguments: string(argsJSON), Status: status,
		})
	}
	select {
	case <-ctx.Done():
		publishCompletion("cancelled")
		return nil, ctx.Err()
	case <-timer.C:
		publishCompletion("completed")
		// Empty answers map (one per question, all blank) so the
		// model sees the full schema and doesn't loop reasking.
		out := make(map[string]string, len(args.Questions))
		for _, q := range args.Questions {
			out[q.Question] = ""
		}
		return out, nil
	case raw := <-respCh:
		publishCompletion("completed")
		return parseAnswerPayload(raw, args.Questions), nil
	}
}

// SetMode transitions the per-thread agent state mode. Narrow
// UPDATE — preserves StartedAt/DurationLimit/RoundCount that
// concurrent autonomous-loop bookkeeping owns.
func (a *toolAgent) SetMode(ctx context.Context, mode string) error {
	var m storage.AgentMode
	switch mode {
	case "plan":
		m = storage.AgentModePlan
	case "autonomous":
		m = storage.AgentModeAutonomous
	}
	if err := a.db.SetAgentStatusAndMode(a.threadID, storage.AgentStatusRunning, m); err != nil {
		return err
	}
	gqlMode := AgentModeNormal
	if m == storage.AgentModeAutonomous {
		gqlMode = AgentModeAutonomous
	} else if m == storage.AgentModePlan {
		gqlMode = AgentModePlan
	}
	// Carry the current planContent across publishes — ExitPlan
	// fires OnPlanContent then SetMode("normal") in sequence, and
	// without this carry the second publish's empty planContent
	// would clobber the first and the frontend PlanPanel wouldn't
	// render.
	a.pubsub.PublishAgentState(AgentStateUpdate{
		ThreadID: a.threadID, Status: AgentStatusRunning, Mode: gqlMode,
		PlanContent: a.planStore.GetPlan(a.threadID),
	})
	return nil
}

// IsPlanMode reads the per-thread mode from the DB. Used by the
// plan-guard on every write-tool call.
func (a *toolAgent) IsPlanMode() bool {
	st, _ := a.db.GetAgentState(a.threadID)
	return st != nil && st.Mode == storage.AgentModePlan
}

// OnPlanContent publishes the compiled plan to subscribers.
// Mode stays Plan — ExitPlan is just surfacing; the user
// explicitly flips mode via approvePlan / rejectPlan mutations.
func (a *toolAgent) OnPlanContent(content string) {
	a.planStore.SetPlan(a.threadID, content)
	a.pubsub.PublishAgentState(AgentStateUpdate{
		ThreadID: a.threadID, Status: AgentStatusIdle, Mode: AgentModePlan,
		PlanContent: content,
	})
}

// RequireApproval blocks for user approval on a tool call. Publishes
// pending/cancelled/timeout/approved/denied via pubsub so the UI
// renders a button + status; the user's click resolves the
// Approvals.RegisterApproval channel.
func (a *toolAgent) RequireApproval(ctx context.Context, callID, toolName, argsJSON string) (bool, error) {
	a.pubsub.PublishToolExec(ToolExec{
		ThreadID: a.threadID, CallID: callID, ToolName: toolName,
		Arguments: argsJSON, Status: "pending",
	})
	ch, unregister := a.approvals.RegisterApproval(callID, a.threadID)
	defer unregister()
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	publish := func(status string) {
		a.pubsub.PublishToolExec(ToolExec{
			ThreadID: a.threadID, CallID: callID, ToolName: toolName,
			Arguments: argsJSON, Status: status,
		})
	}
	select {
	case <-ctx.Done():
		publish("cancelled")
		return false, ctx.Err()
	case <-timer.C:
		publish("timeout")
		return false, fmt.Errorf("tool approval timed out after 5 minutes")
	case approved := <-ch:
		if approved {
			publish("approved")
		} else {
			publish("denied")
		}
		return approved, nil
	}
}

// FireHook runs configured user hooks. Returns an error iff a hook
// blocked the tool call.
func (a *toolAgent) FireHook(ctx context.Context, event, toolName string) error {
	if a.hooks == nil {
		return nil
	}
	result, err := a.hooks.Fire(ctx, event, toolName)
	if err != nil {
		return err
	}
	if result != nil && result.Blocked {
		return fmt.Errorf("hook blocked %s on %s: %s", event, toolName, result.Output)
	}
	return nil
}

// parseAnswerPayload interprets the raw answer string the frontend
// sent to answerQuestion. For multi-question tool calls the
// frontend sends a JSON object keyed by question text; for a single
// question it may send either a JSON map or a plain string. Both
// shapes are accepted; missing questions resolve to empty strings
// so the model sees a complete map.
func parseAnswerPayload(raw string, questions []tools.AskUserQuestion) map[string]string {
	var asMap map[string]string
	if err := json.Unmarshal([]byte(raw), &asMap); err == nil && asMap != nil {
		out := make(map[string]string, len(questions))
		for _, q := range questions {
			out[q.Question] = asMap[q.Question]
		}
		return out
	}
	out := make(map[string]string, len(questions))
	for i, q := range questions {
		if i == 0 {
			out[q.Question] = raw
		} else {
			out[q.Question] = ""
		}
	}
	return out
}
