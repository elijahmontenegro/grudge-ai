package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/emontenegr/grudge/service/sandbox"
	"google.golang.org/adk/tool"
)

// Tool catalog construction. tools.go has been decomposed by family
// (bash / file / search / web / ask / skill / plan / agent / task);
// each family lives in tools_<family>.go and contributes via its own
// register function. This file owns the shell — the buildCtx that
// carries shared closures, BuildTools that drives registration, and
// the cross-cutting permission / plan-guard / approval helpers.

const (
	PermAllow = "allow"
	PermAsk   = "ask"
	PermDeny  = "deny"
)

// DefaultPermission returns the default permission for a tool.
func DefaultPermission(toolName string) string {
	defaults := map[string]string{
		"FileRead": PermAllow, "Glob": PermAllow, "Grep": PermAllow,
		"WebSearch": PermAllow, "WebFetch": PermAllow, "AskUserQuestion": PermAllow,
		"FileEdit": PermAsk, "FileWrite": PermAsk, "Bash": PermAsk,
		"NotebookEdit": PermAsk, "Agent": PermAsk, "SendMessage": PermAsk,
		"EnterPlanMode": PermAsk, "ExitPlanMode": PermAsk,
		"Skill": PermAsk, "TodoWrite": PermAsk,
		"TaskCreate": PermAsk, "TaskGet": PermAllow, "TaskUpdate": PermAsk,
		"TaskList": PermAllow, "TaskStop": PermAsk, "TaskOutput": PermAllow,
	}
	if p, ok := defaults[toolName]; ok {
		return p
	}
	return PermAsk
}

// ToolDeps is the per-thread bundle every tool needs at
// construction time. The capability fields (SubAgent / Asker /
// Mode / Approver / HookFirer) are runner-side operations the
// runtime supplies; the remaining fields are static per-thread
// data (workspace, paths, permissions, skills) that tools read but
// don't call into.
//
// Splitting "operations" (interfaces) from "data" (the remaining
// fields) gives compile-time satisfaction-checking on the
// operations — a runner that doesn't implement every method fails
// to compile here instead of crashing later on a nil-closure deref.
type ToolDeps struct {
	// Per-capability handles into the runtime. Each is satisfied
	// structurally by the runtime's *toolAgent value; tools depend
	// only on the capabilities they use (see contract.go).
	SubAgent  SubAgentOps
	Asker     UserAsker
	Mode      ModeOps
	Approver  Approver
	HookFirer HookFirer

	// Sandboxed routes Bash through the Docker sandbox AND confines
	// every file tool to Workspace. Prompt-injected paths cannot reach
	// the host when this is true. When false, tools run on the host
	// with the user's shell ambient permissions — same as before.
	Sandboxed bool
	// Workspace is the absolute host path bind-mounted into the
	// container at sandbox.ContainerWorkspace. Required when Sandboxed
	// is true. Obtained from sandbox.WorkspaceDir.
	Workspace string
	// WorkingDirs are host directories the non-sandboxed Bash uses for
	// cwd / glob / grep. Ignored on sandboxed threads — those only see
	// the Workspace.
	WorkingDirs []string
	Tasks       *TaskStore
	Skills      []SkillDef
	// Search provider (SearXNG or compatible)
	SearchURL string // e.g. "http://localhost:8888" — empty if not configured
	// Agent/plan mode dependencies
	ThreadID string
	PlanDir  string // $XDG_DATA_HOME/grudge/plans/
	// Configured permissions — overrides DefaultPermission when set
	Permissions map[string]string
}

// SkillDef is a minimal skill reference for the tool.
type SkillDef struct {
	Name    string
	Content string
}

// ErrPlanModeWriteDisabled is returned when a write tool is called during plan mode.
var ErrPlanModeWriteDisabled = fmt.Errorf("write tools are disabled in plan mode — use read tools to explore, then exit plan mode to execute")

// buildCtx bundles the shared closures and state every tool needs at
// construction time. Built once per BuildTools call; passed to each
// family's register function.
type buildCtx struct {
	deps    ToolDeps
	baseDir string

	resolvePath     func(string) (string, error)
	planGuard       func(path string) error
	requireApproval func(ctx context.Context, toolName, argsJSON string) error
	fireHook        func(ctx context.Context, event, toolName string) error
	execCmd         func(ctx context.Context, command, dir string) (string, int)

	tools   *[]tool.Tool
	addTool func(name string, t tool.Tool, err error) error
}

// BuildTools creates all ADK FunctionTools for the agent. Per-family
// register functions in tools_<family>.go contribute to the catalog
// in fixed order — order is observable in the tool list shown to the
// model, so we keep it stable.
func BuildTools(deps ToolDeps) ([]tool.Tool, error) {
	ctx := newBuildCtx(deps)

	registrations := []func(*buildCtx) error{
		registerBashTool,
		registerFileReadTool,
		registerSearchTools,
		registerWebTools,
		registerAskUserTool,
		registerWriteTools,
		registerSkillTools,
		registerPlanTools,
		registerAgentTools,
		registerTaskTools,
	}
	for _, fn := range registrations {
		if err := fn(ctx); err != nil {
			return nil, err
		}
	}
	return *ctx.tools, nil
}

func newBuildCtx(deps ToolDeps) *buildCtx {
	// baseDir is the "current working directory" for tools that need one
	// (Glob/Grep when the caller passes no path, Bash on non-sandboxed
	// threads). Sandboxed threads pin this to the workspace; non-
	// sandboxed threads use the user's configured WorkingDirs[0].
	baseDir := ""
	if deps.Sandboxed {
		baseDir = deps.Workspace
	} else if len(deps.WorkingDirs) > 0 {
		baseDir = deps.WorkingDirs[0]
	}

	// resolvePath is the single gate for every file tool on sandboxed
	// threads — it either returns the absolute host path inside the
	// workspace or an error the tool surfaces to the model. On non-
	// sandboxed threads it passes the raw path through unchanged so
	// existing host-side behavior is preserved.
	resolvePath := func(p string) (string, error) {
		if !deps.Sandboxed {
			return p, nil
		}
		return sandbox.ResolveWorkspacePath(deps.Workspace, p)
	}

	// planGuard returns an error if plan mode is active. Called by all
	// write tools. Writes to the plan directory are exempt — the agent
	// needs to write plan artifacts.
	planGuard := func(path string) error {
		if !deps.Mode.IsPlanMode() {
			return nil
		}
		if deps.PlanDir != "" && path != "" && strings.HasPrefix(filepath.Clean(path), filepath.Clean(deps.PlanDir)) {
			return nil
		}
		return ErrPlanModeWriteDisabled
	}

	callSeq := &atomic.Int64{}

	// requireApproval checks the tool's permission (configured, then
	// default) and delegates the blocking gate to the agent.
	requireApproval := func(ctx context.Context, toolName, argsJSON string) error {
		perm := DefaultPermission(toolName)
		if p, ok := deps.Permissions[toolName]; ok {
			perm = p
		}
		if perm == PermDeny {
			return fmt.Errorf("tool %s is denied by permission policy", toolName)
		}
		if perm != PermAsk {
			return nil
		}
		callID := fmt.Sprintf("call-%s-%d", toolName, callSeq.Add(1))
		approved, err := deps.Approver.RequireApproval(ctx, callID, toolName, argsJSON)
		if err != nil {
			return fmt.Errorf("approval: %w", err)
		}
		if !approved {
			return fmt.Errorf("tool %s denied by user", toolName)
		}
		return nil
	}

	fireHook := func(ctx context.Context, event, toolName string) error {
		return deps.HookFirer.FireHook(ctx, event, toolName)
	}

	execCmd := func(ctx context.Context, command, dir string) (string, int) {
		if deps.Sandboxed {
			sb := sandbox.New(deps.Workspace)
			out, err := sb.Exec(ctx, command)
			if err != nil {
				return out, 1
			}
			return out, 0
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		if dir != "" {
			cmd.Dir = dir
		}
		out, err := cmd.CombinedOutput()
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}
		return string(out), exitCode
	}

	var tools []tool.Tool
	addTool := func(name string, t tool.Tool, err error) error {
		if err != nil {
			return fmt.Errorf("tool %s: %w", name, err)
		}
		tools = append(tools, t)
		return nil
	}

	return &buildCtx{
		deps:            deps,
		baseDir:         baseDir,
		resolvePath:     resolvePath,
		planGuard:       planGuard,
		requireApproval: requireApproval,
		fireHook:        fireHook,
		execCmd:         execCmd,
		tools:           &tools,
		addTool:         addTool,
	}
}

// marshalArgs is shared shorthand: every tool that needs an approval
// argsJSON marshals its args struct here. Errors are swallowed —
// approval is best-effort surfacing of the args, not a security
// boundary, and a malformed args struct is the wrong place to fail
// the tool call.
func marshalArgs(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
