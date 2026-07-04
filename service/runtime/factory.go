package runtime

import (
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/elijahmontenegro/grudge/adkbridge"
	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/core/httpc/retry"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/rrc/tokenscale"
	"github.com/elijahmontenegro/grudge/service/agent"
	"github.com/elijahmontenegro/grudge/service/agent/tools"
	"github.com/elijahmontenegro/grudge/service/config"
	"github.com/elijahmontenegro/grudge/service/datadir"
	"github.com/elijahmontenegro/grudge/service/hooks"
	"github.com/elijahmontenegro/grudge/service/messages"
	"github.com/elijahmontenegro/grudge/service/prompt"
	"github.com/elijahmontenegro/grudge/service/skills"
	"github.com/elijahmontenegro/grudge/service/storage"

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
	Inserter  *messages.Inserter

	Pubsub        Pubsub
	Approvals     Approvals
	PlanStore     PlanStore
	Selections    Selections
	EmbedEnqueuer EmbedEnqueuer

	// Scales is the process-wide token-scale store (learned per-model
	// counter→model ratios, grounded in provider-reported usage).
	// Owned by the composition root, not the settings-rebuild
	// lifecycle — the store is keyed by adapter/model and survives
	// provider swaps. Nil runs ungrounded.
	Scales *tokenscale.Store
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

	workspace, err := datadir.WorkspaceDir(deps.Config.DataDir, threadID)
	if err != nil {
		return nil, fmt.Errorf("workspace dir: %w", err)
	}

	skillDefs := make([]tools.SkillDef, 0, len(deps.Skills))
	for _, s := range deps.Skills {
		skillDefs = append(skillDefs, tools.SkillDef{Name: s.Name, Content: s.Content})
	}

	searchURL := ""
	if c, ok := deps.Config.Settings.Providers["search"]; ok {
		searchURL = c.BaseURL
	}

	toolList, err := tools.BuildTools(buildToolDeps(threadID, thread, workspace, skillDefs, searchURL, deps))
	if err != nil {
		return nil, fmt.Errorf("build tools: %w", err)
	}
	toolList = append(toolList, deps.MCPTools...)

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
	mainWithRetry := retry.NewCompleter(deps.Main, retry.DefaultPolicy(), func(ev retry.Event) {
		deps.Pubsub.PublishRetry(threadID, ev)
	})
	rerankerModelID := ""
	if c, ok := deps.Config.Settings.Providers["scorer"]; ok {
		rerankerModelID = c.Model
	}
	// Token grounding for the main completer: the scale handle is
	// pre-bound to adapter/model (the codec is part of the scale — the
	// same model behind two adapters sends different subsets), and the
	// counting projection comes from the adapter's own registration.
	// Both nil when no main provider is configured — ungrounded,
	// count-everything, today's exact behavior.
	var scales adkbridge.TokenScales
	var countText func(m *llmv1.LLMMessage) string
	if c, ok := deps.Config.Settings.Providers["main"]; ok {
		if deps.Scales != nil && c.Adapter != "" && c.Model != "" {
			if bound := deps.Scales.Bound(c.Adapter + "/" + c.Model); bound != nil {
				scales = bound
			}
		}
		countText = core.CountProjection(c.Adapter)
	}
	runner, err := agent.NewRunner(deps.Engine, mainWithRetry, deps.DB, threadID, toolList, modelName, instruction, rerankerModelID, deps.Inserter, scales, countText)
	if err != nil {
		return nil, err
	}

	wireRunnerCallbacks(runner, threadID, deps)

	return &Entry{Runner: runner}, nil
}

// buildToolDeps assembles tools.ToolDeps from runtime Deps + the
// per-thread fixings. Operational dependencies live behind the
// per-capability tools interfaces (SubAgent / Asker / Mode /
// Approver / HookFirer); a single *toolAgent satisfies all five
// structurally and is shared across the fields. Remaining fields
// are static per-thread data (workspace, paths, skills, permissions).
func buildToolDeps(threadID string, thread *threadv1.Thread, workspace string, skillDefs []tools.SkillDef, searchURL string, deps Deps) tools.ToolDeps {
	ta := newToolAgent(threadID, deps)
	return tools.ToolDeps{
		SubAgent:    ta,
		Asker:       ta,
		Mode:        ta,
		Approver:    ta,
		HookFirer:   ta,
		Sandboxed:   thread.Sandboxed,
		Workspace:   workspace,
		WorkingDirs: thread.WorkingDirs,
		Tasks:       tools.NewTaskStore(),
		ThreadID:    threadID,
		Skills:      skillDefs,
		SearchURL:   searchURL,
		PlanDir:     filepath.Join(deps.Config.DataDir, "plans"),
		Permissions: deps.Config.Settings.Permissions,
	}
}

// assembleInstruction produces the system prompt for the runner via
// the prompt assembler. Failure here is fatal: an empty instruction
// would leave the agent without any system context.
func assembleInstruction(threadID string, thread *threadv1.Thread, deps Deps) (string, error) {
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
	agentsMD := prompt.LoadAgentsMD(thread.WorkingDirs)
	planDir, err := datadir.PlanDirForThread(deps.Config.DataDir, threadID)
	if err != nil {
		return "", fmt.Errorf("plan dir: %w", err)
	}
	return deps.Assembler.Assemble(prompt.TemplateData{
		UserName:    deps.Config.Settings.GetUserName(),
		ThreadName:  thread.Name,
		Sandboxed:   thread.Sandboxed,
		WorkingDirs: thread.WorkingDirs,
		AgentsMD:    agentsMD,
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
	runner.SetSelectionCallback(func(result *rrcv1.SelectionResult) {
		deps.Selections.Record(result)
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

	// Embed-on-arrival is owned by deps.Inserter — Bootstrap wires the
	// inserter with the EmbedEnqueuer closure so chunk derivation +
	// InsertMessage + Enqueue happen in one place. No per-runner hook
	// here; Runner.indexMessage routes through the inserter directly.

	// Per spec (docs/spec/web/MANIFEST.adoc:187): autonomous errors pause
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
