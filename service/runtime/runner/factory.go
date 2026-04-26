package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/emontenegr/spidey/core"
	completerretry "github.com/emontenegr/spidey/core/completer/retry"
	"github.com/emontenegr/spidey/core/retry"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/adoc"
	"github.com/emontenegr/spidey/service/agent"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/hooks"
	"github.com/emontenegr/spidey/service/prompt"
	"github.com/emontenegr/spidey/service/sandbox"
	"github.com/emontenegr/spidey/service/skills"
	"github.com/emontenegr/spidey/service/storage"

	"google.golang.org/adk/tool"
)

// Deps bundles the cross-thread dependencies the runner factory
// needs. Per-thread inputs (threadID) flow through Build's argument
// list, not Deps.
type Deps struct {
	Registry  *Registry
	DB        *storage.DB
	Engine    *rrc.Engine
	Config    *config.Config
	Main      core.Completer
	Assembler *prompt.Assembler
	Hooks     *hooks.Dispatcher
	Skills    []skills.Skill
	MCPTools  []tool.Tool

	Pubsub        Pubsub
	Approvals     Approvals
	PlanStore     PlanStore
	Selections    Selections
	EmbedEnqueuer EmbedEnqueuer
}

// Build constructs an Entry (runner + lifecycle handles) for
// threadID. The caller is responsible for storing the returned
// entry in deps.Registry — Build does not touch the registry
// itself, so the caller can hold whatever lock it likes around
// the lookup-then-create dance.
func Build(threadID string, deps Deps) (*Entry, error) {
	thread, err := deps.DB.GetThread(threadID)
	if err != nil {
		return nil, fmt.Errorf("get thread %s: %w", threadID, err)
	}

	workspace, err := sandbox.WorkspaceDir(deps.Config.DataDir, threadID)
	if err != nil {
		return nil, fmt.Errorf("workspace dir: %w", err)
	}

	askCh := make(chan agent.AskRequest, 1)
	go runAskLoop(threadID, askCh, deps.Pubsub, deps.Approvals)

	skillDefs := make([]agent.SkillDef, 0, len(deps.Skills))
	for _, s := range deps.Skills {
		skillDefs = append(skillDefs, agent.SkillDef{Name: s.Name, Content: s.Content})
	}

	searchURL := ""
	if c, ok := deps.Config.Settings.Providers["search"]; ok {
		searchURL = c.BaseURL
	}

	tools, err := agent.BuildTools(buildToolDeps(threadID, thread, workspace, askCh, skillDefs, searchURL, deps))
	if err != nil {
		return nil, fmt.Errorf("build tools: %w", err)
	}
	tools = append(tools, deps.MCPTools...)

	modelName := ""
	if c, ok := deps.Config.Settings.Providers["main"]; ok {
		modelName = c.Model
	}

	instruction, err := assembleInstruction(threadID, thread, deps)
	if err != nil {
		return nil, err
	}

	// Wrap the main completer with retry. Closes over threadID so
	// transient failures surface as RetryStatus events on this
	// thread's AgentState subscription — the UI renders the
	// indicator contextually.
	mainWithRetry := completerretry.New(deps.Main, retry.DefaultPolicy(), func(ev retry.Event) {
		deps.Pubsub.PublishRetry(threadID, ev)
	})
	rerankerModelID := ""
	if c, ok := deps.Config.Settings.Providers["classifier"]; ok {
		rerankerModelID = c.Model
	}
	runner, err := agent.NewRunner(deps.Engine, mainWithRetry, deps.DB, threadID, tools, modelName, instruction, rerankerModelID)
	if err != nil {
		return nil, err
	}

	wireRunnerCallbacks(runner, threadID, deps)

	return &Entry{Runner: runner, AskCh: askCh}, nil
}

// runAskLoop drains askCh in a goroutine — one AskUserQuestion at a
// time. For each request it publishes a "waiting_for_user" tool
// execution, registers an answer channel via Approvals, and waits
// 5 minutes for the answer (or empty answers on timeout).
//
// The payload published to the frontend is the JSON-marshaled
// AskUserArgs so the UI has everything it needs to render option
// lists without a second round-trip. The frontend answers via
// answerQuestion(callId, answer) where `answer` is JSON of the
// question→answer map (or plain text when there's only one
// question).
func runAskLoop(threadID string, askCh chan agent.AskRequest, pub Pubsub, approvals Approvals) {
	for req := range askCh {
		callID := fmt.Sprintf("ask-%d", time.Now().UnixNano())
		argsJSON, err := json.Marshal(req.Args)
		if err != nil {
			argsJSON = []byte(`{"questions":[]}`)
		}
		pub.PublishToolExec(ToolExec{
			ThreadID: threadID, CallID: callID, ToolName: "AskUserQuestion",
			Arguments: string(argsJSON), Status: "waiting_for_user",
		})
		respCh, unregister := approvals.RegisterAnswer(callID)
		timer := time.NewTimer(5 * time.Minute)
		var answers map[string]string
		select {
		case <-timer.C:
			answers = map[string]string{}
		case raw := <-respCh:
			answers = parseAnswerPayload(raw, req.Args.Questions)
		}
		timer.Stop()
		req.RespCh <- answers
		pub.PublishToolExec(ToolExec{
			ThreadID: threadID, CallID: callID, ToolName: "AskUserQuestion",
			Arguments: string(argsJSON), Status: "completed",
		})
		unregister()
	}
}

// parseAnswerPayload interprets the raw answer string the frontend
// sent to answerQuestion. For multi-question tool calls the
// frontend sends a JSON object keyed by question text; for a single
// question it may send either a JSON map or a plain string. Both
// shapes are accepted; missing questions resolve to empty strings
// so the model sees a complete map.
func parseAnswerPayload(raw string, questions []agent.AskUserQuestion) map[string]string {
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

// buildToolDeps assembles agent.ToolDeps from runtime Deps + the
// per-thread fixings. The closures embedded here are how tools
// reach back into the kernel — for spawn/send subagent, approval
// gating, hook dispatch, and plan/agent state transitions.
func buildToolDeps(threadID string, thread *pb.Thread, workspace string, askCh chan agent.AskRequest, skillDefs []agent.SkillDef, searchURL string, deps Deps) agent.ToolDeps {
	return agent.ToolDeps{
		Sandboxed:   thread.Sandboxed,
		Workspace:   workspace,
		WorkingDirs: thread.WorkingDirs,
		Tasks:       agent.NewTaskStore(),
		ThreadID:    threadID,
		Skills:      skillDefs,
		SearchURL:   searchURL,
		IsPlanMode: func() bool {
			st, _ := deps.DB.GetAgentState(threadID)
			return st != nil && st.Mode == storage.AgentModePlan
		},
		AgentState: func(mode string) error {
			var m storage.AgentMode
			switch mode {
			case "plan":
				m = storage.AgentModePlan
			case "autonomous":
				m = storage.AgentModeAutonomous
			}
			// Narrow UPDATE so an EnterPlan/ExitPlan call during an
			// autonomous run doesn't wipe StartedAt/DurationLimit/
			// RoundCount — readers would see stale metadata.
			if err := deps.DB.SetAgentStatusAndMode(threadID, storage.AgentStatusRunning, m); err != nil {
				return err
			}
			gqlMode := AgentModeNormal
			if m == storage.AgentModeAutonomous {
				gqlMode = AgentModeAutonomous
			} else if m == storage.AgentModePlan {
				gqlMode = AgentModePlan
			}
			// Preserve current planContent across publishes. ExitPlan
			// fires OnPlanContent then AgentState("normal") in
			// sequence — without this, the second publish's empty
			// planContent clobbers the first, and the frontend
			// PlanPanel never renders.
			deps.Pubsub.PublishAgentState(AgentStateUpdate{
				ThreadID: threadID, Status: AgentStatusRunning, Mode: gqlMode,
				PlanContent: deps.PlanStore.GetPlan(threadID),
			})
			return nil
		},
		CompileAdoc: func(path string) (string, error) {
			return adoc.Compile(path)
		},
		PlanDir: filepath.Join(deps.Config.DataDir, "plans"),
		SpawnAgent: func(ctx context.Context, task, forkID string) (string, error) {
			entry, ok := deps.Registry.Get(threadID)
			if !ok {
				return "", fmt.Errorf("no runner for thread %s", threadID)
			}
			fork, err := entry.Runner.SpawnSubagent(ctx, task, forkID)
			if err != nil {
				return "", err
			}
			deps.Registry.Set(forkID, &Entry{
				Runner:         fork,
				ParentThreadID: threadID,
			})
			deps.Pubsub.PublishSubagent(SubagentEvent{
				ThreadID: threadID, ForkThreadID: forkID,
				Task: task, Status: "running",
			})
			return "Subagent started: " + forkID, nil
		},
		SendToAgent: func(ctx context.Context, agentID, message string) (string, error) {
			entry, ok := deps.Registry.Get(agentID)
			if !ok {
				return "", fmt.Errorf("no runner for agent %s", agentID)
			}
			resp, err := entry.Runner.SendMessage(ctx, message, pb.SelectionScope_SELECTION_SCOPE_THREAD)
			if err != nil {
				return "", err
			}
			// Incremental merge after each subagent interaction —
			// edges and scores accumulate in the parent. Merge is
			// idempotent (scores use INSERT OR REPLACE).
			if entry.ParentThreadID != "" {
				if parentEntry, pOk := deps.Registry.Get(entry.ParentThreadID); pOk {
					parentEntry.Runner.MergeSubagent(entry.Runner)
				}
			}
			return fmt.Sprintf("Response from %s: %d blocks", agentID, len(resp.Content)), nil
		},
		ApprovalFn: func(ctx context.Context, callID, toolName, args string) (bool, error) {
			deps.Pubsub.PublishToolExec(ToolExec{
				ThreadID: threadID, CallID: callID, ToolName: toolName,
				Arguments: args, Status: "pending",
			})
			ch, unregister := deps.Approvals.RegisterApproval(callID, threadID)
			defer unregister()
			timer := time.NewTimer(5 * time.Minute)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				deps.Pubsub.PublishToolExec(ToolExec{
					ThreadID: threadID, CallID: callID, ToolName: toolName,
					Arguments: args, Status: "cancelled",
				})
				return false, ctx.Err()
			case <-timer.C:
				deps.Pubsub.PublishToolExec(ToolExec{
					ThreadID: threadID, CallID: callID, ToolName: toolName,
					Arguments: args, Status: "timeout",
				})
				return false, fmt.Errorf("tool approval timed out after 5 minutes")
			case approved := <-ch:
				status := "approved"
				if !approved {
					status = "denied"
				}
				deps.Pubsub.PublishToolExec(ToolExec{
					ThreadID: threadID, CallID: callID, ToolName: toolName,
					Arguments: args, Status: status,
				})
				return approved, nil
			}
		},
		HookFn: func(ctx context.Context, event, toolName string) error {
			if deps.Hooks == nil {
				return nil
			}
			result, err := deps.Hooks.Fire(ctx, event, toolName)
			if err != nil {
				return err
			}
			if result != nil && result.Blocked {
				return fmt.Errorf("hook blocked %s on %s: %s", event, toolName, result.Output)
			}
			return nil
		},
		Permissions: deps.Config.Settings.Permissions,
		AskCh:       askCh,
		OnPlanContent: func(content string) {
			deps.PlanStore.SetPlan(threadID, content)
			// Mode stays Plan — ExitPlan used to flip to Normal here
			// but now it just surfaces the plan. The user flips mode
			// explicitly via approvePlan (to Normal/Autonomous) or
			// keeps iterating (stays Plan). Status stays Idle since
			// the model's turn is either about to end naturally or
			// the runner is between rounds.
			deps.Pubsub.PublishAgentState(AgentStateUpdate{
				ThreadID: threadID, Status: AgentStatusIdle, Mode: AgentModePlan,
				PlanContent: content,
			})
		},
	}
}

// assembleInstruction produces the system prompt for the runner via
// the prompt assembler. Failure here is fatal: an empty instruction
// would leave the agent without any system context.
func assembleInstruction(threadID string, thread *pb.Thread, deps Deps) (string, error) {
	if deps.Assembler == nil {
		return "", fmt.Errorf("prompt assembler not initialized — templates directory missing")
	}
	st, _ := deps.DB.GetAgentState(threadID)
	mode := "normal"
	if st != nil {
		switch st.Mode {
		case storage.AgentModeAutonomous:
			mode = "autonomous"
		case storage.AgentModePlan:
			mode = "plan"
		}
	}
	spideyMD := prompt.LoadSpideyMD(thread.WorkingDirs)
	planDir, err := PlanDirForThread(deps.Config.DataDir, threadID)
	if err != nil {
		return "", fmt.Errorf("plan dir: %w", err)
	}
	return deps.Assembler.Assemble(prompt.TemplateData{
		UserName:    deps.Config.Settings.GetUserName(),
		ThreadName:  thread.Name,
		Sandboxed:   thread.Sandboxed,
		WorkingDirs: thread.WorkingDirs,
		SpideyMD:    spideyMD,
		PlanDir:     planDir,
		CurrentTime: time.Now().Format(time.RFC3339),
		Mode:        mode,
	})
}

// wireRunnerCallbacks attaches every runner-emitted event to the
// pubsub / selections / embed surfaces.
func wireRunnerCallbacks(runner *agent.Runner, threadID string, deps Deps) {
	runner.SetStreamCallback(func(delta, thinking string, done bool) {
		deps.Pubsub.PublishStream(StreamDelta{
			ThreadID: threadID, MessageID: threadID,
			Delta: delta, Thinking: thinking, Done: done,
		})
	})

	// In-memory citation tally + DB persistence on every selection.
	// The hot-path read against the in-memory map happens off this
	// path (the resolver implementation reads directly).
	runner.SetSelectionCallback(func(result *pb.SelectionResult) {
		deps.Selections.RecordSelection(threadID, result)
	})

	runner.SetRoundCallback(func(round int, elapsed time.Duration) {
		elapsedStr := elapsed.Truncate(time.Second).String()
		durLimit := ""
		if st, _ := deps.DB.GetAgentState(threadID); st != nil {
			durLimit = st.DurationLimit
		}
		deps.Pubsub.PublishAgentState(AgentStateUpdate{
			ThreadID: threadID, Status: AgentStatusRunning, Mode: AgentModeAutonomous,
			RoundCount: round, ElapsedTime: elapsedStr, DurationLimit: durLimit,
		})
		// Narrow UPDATE so we don't wipe StartedAt/DurationLimit set
		// by StartAutonomous at the beginning of the run.
		deps.DB.SetAgentRoundCount(threadID, round)
	})

	runner.OnToolCall = func(callID, toolName, args string) {
		deps.Pubsub.PublishToolExec(ToolExec{
			ThreadID: threadID, CallID: callID, ToolName: toolName,
			Arguments: args, Status: "running",
		})
	}
	runner.OnToolResult = func(callID, toolName, result string, isError bool) {
		status := "completed"
		if isError {
			status = "failed"
		}
		deps.Pubsub.PublishToolExec(ToolExec{
			ThreadID: threadID, CallID: callID, ToolName: toolName,
			Status: status, Result: result, IsError: isError,
		})
	}

	// Embed-on-arrival for search indexing. Each message's chunks
	// get embedded as soon as it's stored so RRC's cosine prefilter
	// has vectors ready by the time the next OnMessage call fires.
	runner.OnMessageStored = func(msgID, _text string) {
		if deps.EmbedEnqueuer != nil {
			deps.EmbedEnqueuer.Enqueue(msgID)
		}
	}

	// Per spec (spec/web/MANIFEST.adoc:187): autonomous errors pause
	// the run rather than exit it. Handler pauses the autoState so
	// the loop blocks at the next waitIfPaused, marks the DB row
	// Paused, and publishes Paused to the UI as a retry-final event.
	// The UI renders RetryStatus{Final:true, Error:...} as a pause
	// banner with the error text — no corpus write needed (a system
	// message in the corpus would pollute future Selection rounds).
	runner.OnAutonomousError = func(err error) {
		log.Printf("[Autonomous] mid-run error on %s: %v — pausing", threadID, err)
		runner.PauseAutonomous()
		_ = deps.DB.SetAgentStatus(threadID, storage.AgentStatusPaused)
		deps.Pubsub.PublishAgentState(AgentStateUpdate{
			ThreadID: threadID, Status: AgentStatusPaused, Mode: AgentModeAutonomous,
			Retry: &RetryStatus{
				Attempt: 1, MaxAttempts: 1,
				Error: err.Error(),
				Final: true,
			},
		})
	}
}
