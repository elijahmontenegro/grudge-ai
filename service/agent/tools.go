package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/emontenegr/spidey/service/sandbox"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

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

// --- Tool argument/result types ---

type BashArgs struct {
	Command string `json:"command"`
}
type BashResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

type FileReadArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type FileReadResult struct {
	Output string `json:"output"`
}

type FileEditArgs struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}
type FileEditResult struct {
	Success bool `json:"success"`
}

type FileWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type FileWriteResult struct {
	Success bool   `json:"success"`
	Path    string `json:"path,omitempty"`
}

type GlobArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}
type GlobResult struct {
	Files []string `json:"files"`
}

type GrepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}
type GrepResult struct {
	Matches []string `json:"matches"`
}

type NotebookEditArgs struct {
	Path    string `json:"path"`
	CellIdx int    `json:"cell_index"`
	Content string `json:"content"`
}
type NotebookEditResult struct {
	Success bool `json:"success"`
}

type WebSearchArgs struct {
	Query string `json:"query"`
}
type WebSearchResult struct {
	Results []string `json:"results"`
}

type WebFetchArgs struct {
	URL string `json:"url"`
}
type WebFetchResult struct {
	Output     string `json:"output"`
	StatusCode int    `json:"status_code"`
}

// AskUserOption is one choice offered for a single question. Label is
// what's shown; Description is an optional clarifier beneath the label.
type AskUserOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// AskUserQuestion carries one prompt. Structure mirrors Claude Code's
// AskUserQuestion tool so the same prompting conventions carry over:
// a short `header` (like a setting name), the question itself, optional
// `options` (2–4 concise choices), and `multiSelect` when multiple
// answers are allowed at once.
type AskUserQuestion struct {
	Question    string          `json:"question"`
	Header      string          `json:"header,omitempty"`
	Options     []AskUserOption `json:"options,omitempty"`
	MultiSelect bool            `json:"multiSelect,omitempty"`
}

// AskUserArgs accepts 1–4 questions in a single tool call. The UI walks
// through them sequentially and batches answers back as one map.
type AskUserArgs struct {
	Questions []AskUserQuestion `json:"questions"`
}

// AskUserResult returns the user's answers keyed by question text so the
// model can correlate each answer with the question it corresponds to
// without positional matching.
type AskUserResult struct {
	Answers map[string]string `json:"answers"`
}

type SkillArgs struct {
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
}
type SkillResult struct {
	Output string `json:"output"`
}

type TodoWriteArgs struct {
	Tasks []string `json:"tasks"`
}
type TodoWriteResult struct {
	Success bool `json:"success"`
}

type PlanModeArgs struct {
	ThreadID string `json:"thread_id"`
}
type PlanModeResult struct {
	Success bool `json:"success"`
}

type AgentToolArgs struct {
	Task        string `json:"task"`
	Description string `json:"description,omitempty"`
}
type AgentToolResult struct {
	Result string `json:"result"`
}

type SendMessageArgs struct {
	To      string `json:"to"`
	Message string `json:"message"`
}
type SendMessageResult struct {
	Response string `json:"response"`
}

type TaskCreateArgs struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
}
type TaskCreateResult struct {
	TaskID string `json:"task_id"`
}

type TaskGetArgs struct {
	TaskID string `json:"task_id"`
}
type TaskGetResult struct {
	Subject string `json:"subject"`
	Status  string `json:"status"`
}

type TaskUpdateArgs struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}
type TaskUpdateResult struct {
	Success bool `json:"success"`
}

type TaskListArgs struct{}
type TaskListResult struct {
	Tasks []string `json:"tasks"`
}

type TaskStopArgs struct {
	TaskID string `json:"task_id"`
}
type TaskStopResult struct {
	Success bool `json:"success"`
}

type TaskOutputArgs struct {
	TaskID string `json:"task_id"`
}
type TaskOutputResult struct {
	Output string `json:"output"`
}

// ToolDeps holds dependencies injected into tools that need service access.
type ToolDeps struct {
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
	AskCh       chan<- AskRequest
	// Search provider (SearXNG or compatible)
	SearchURL   string // e.g. "http://localhost:8888" — empty if not configured
	// Agent/plan mode dependencies
	ThreadID    string
	AgentState  func(mode string) error                      // transition agent mode
	IsPlanMode  func() bool                                  // check if plan mode active
	CompileAdoc func(path string) (string, error)            // adoc.Compile
	PlanDir     string                                       // $XDG_DATA_HOME/spidey/plans/
	// Subagent spawner — service wires this to create forked runners
	SpawnAgent func(ctx context.Context, task, forkID string) (string, error)
	// Send message to a running subagent thread
	SendToAgent func(ctx context.Context, agentID, message string) (string, error)
	// Tool approval — blocks until user approves/denies. Returns true if approved.
	ApprovalFn func(ctx context.Context, callID, toolName, args string) (bool, error)
	// Hook firing — called before/after tool execution
	HookFn func(ctx context.Context, event, toolName string) error
	// Configured permissions — overrides DefaultPermission when set
	Permissions map[string]string
	// Plan content callback — called by ExitPlanMode to surface plan to frontend
	OnPlanContent func(content string)
}

// SkillDef is a minimal skill reference for the tool.
type SkillDef struct {
	Name    string
	Content string
}

// AskRequest is sent to the UI when AskUserQuestion is called. The full
// structured args carry through so the UI can render headers, options,
// and multi-select controls. The UI replies with a map of
// question→answer (one key per question in Args.Questions); for a
// single question the map has one entry.
type AskRequest struct {
	Args   AskUserArgs
	RespCh chan map[string]string
}

// askUserExample is an inline valid payload the validator pastes into
// error messages. Smaller Ollama models frequently emit
// `{"questions": null}` or skip required sub-fields on the first try;
// seeing a fully-formed example on the error path lets them copy-adapt
// the structure on retry instead of looping on the same malformed call.
const askUserExample = `Valid example:
{"questions":[
  {"header":"Coffee","question":"How do you take your coffee?","multiSelect":false,"options":[
    {"label":"Black","description":"No milk, no sugar."},
    {"label":"With milk","description":"Splash of milk, no sugar."},
    {"label":"Sweetened","description":"Sugar or syrup added."}
  ]}
]}`

// validateAskUserArgs enforces the Claude Code AskUserQuestion schema
// at runtime. ADK's FunctionTool doesn't carry Zod-style constraints
// through the JSON schema — validation happens here. Every error
// includes the full valid example so the model can self-correct on
// the very next call instead of re-emitting the same malformed args.
func validateAskUserArgs(args AskUserArgs) error {
	n := len(args.Questions)
	if n == 0 {
		return fmt.Errorf(
			"AskUserQuestion: `questions` is required and must contain 1–4 items. "+
				"Do NOT pass `null` or omit the field — always include a fully-populated array.\n\n%s",
			askUserExample)
	}
	if n > 4 {
		return fmt.Errorf("AskUserQuestion: too many questions (%d) — cap is 4.\n\n%s", n, askUserExample)
	}
	seenQ := make(map[string]struct{}, n)
	for i, q := range args.Questions {
		if q.Question == "" {
			return fmt.Errorf("AskUserQuestion: questions[%d].question is required.\n\n%s", i, askUserExample)
		}
		if _, dup := seenQ[q.Question]; dup {
			return fmt.Errorf("AskUserQuestion: questions[%d] duplicates a previous question text.\n\n%s", i, askUserExample)
		}
		seenQ[q.Question] = struct{}{}
		if q.Header == "" {
			return fmt.Errorf(
				"AskUserQuestion: questions[%d].header is required (short chip label ≤20 chars, like \"Coffee\" or \"Library\").\n\n%s",
				i, askUserExample)
		}
		if len(q.Options) < 2 || len(q.Options) > 4 {
			return fmt.Errorf(
				"AskUserQuestion: questions[%d] must have 2–4 options (got %d). Each option needs a `label` and a `description`.\n\n%s",
				i, len(q.Options), askUserExample)
		}
		seenLabel := make(map[string]struct{}, len(q.Options))
		for j, opt := range q.Options {
			if opt.Label == "" {
				return fmt.Errorf("AskUserQuestion: questions[%d].options[%d].label is required.\n\n%s", i, j, askUserExample)
			}
			if _, dup := seenLabel[opt.Label]; dup {
				return fmt.Errorf("AskUserQuestion: questions[%d].options[%d] duplicates a previous label.\n\n%s", i, j, askUserExample)
			}
			seenLabel[opt.Label] = struct{}{}
		}
	}
	return nil
}

// BuildTools creates all ADK FunctionTools for the agent.
// ErrPlanModeWriteDisabled is returned when a write tool is called during plan mode.
var ErrPlanModeWriteDisabled = fmt.Errorf("write tools are disabled in plan mode — use read tools to explore, then exit plan mode to execute")

func BuildTools(deps ToolDeps) ([]tool.Tool, error) {
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

	// planGuard returns an error if plan mode is active. Called by all write tools.
	// Writes to the plan directory are exempt — the agent needs to write plan artifacts.
	planGuard := func(path string) error {
		if deps.IsPlanMode == nil || !deps.IsPlanMode() {
			return nil
		}
		if deps.PlanDir != "" && path != "" && strings.HasPrefix(filepath.Clean(path), filepath.Clean(deps.PlanDir)) {
			return nil // plan directory write is allowed
		}
		return ErrPlanModeWriteDisabled
	}

	callSeq := &atomic.Int64{}

	// requireApproval checks the tool's permission (configured, then default) and blocks for approval if needed.
	requireApproval := func(ctx context.Context, toolName string, argsJSON string) error {
		perm := DefaultPermission(toolName)
		if p, ok := deps.Permissions[toolName]; ok {
			perm = p
		}
		if perm == PermDeny {
			return fmt.Errorf("tool %s is denied by permission policy", toolName)
		}
		if perm != PermAsk || deps.ApprovalFn == nil {
			return nil
		}
		callID := fmt.Sprintf("call-%s-%d", toolName, callSeq.Add(1))
		approved, err := deps.ApprovalFn(ctx, callID, toolName, argsJSON)
		if err != nil {
			return fmt.Errorf("approval: %w", err)
		}
		if !approved {
			return fmt.Errorf("tool %s denied by user", toolName)
		}
		return nil
	}

	// fireHook calls the hook function if available. Errors are fail-closed.
	fireHook := func(ctx context.Context, event, toolName string) error {
		if deps.HookFn == nil {
			return nil
		}
		return deps.HookFn(ctx, event, toolName)
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

	// --- Read tools (Allow) ---

	bash, bashErr := functiontool.New(
		functiontool.Config{Name: "Bash", Description: "Execute a shell command. Returns stdout/stderr and exit code."},
		func(ctx tool.Context, args BashArgs) (BashResult, error) {
			if err := planGuard(""); err != nil {
				return BashResult{Output: err.Error(), ExitCode: 1}, nil
			}
			if err := fireHook(ctx, "PreToolUse", "Bash"); err != nil {
				return BashResult{Output: err.Error(), ExitCode: 1}, nil
			}
			argsJSON, _ := json.Marshal(args)
			if err := requireApproval(ctx, "Bash", string(argsJSON)); err != nil {
				return BashResult{Output: err.Error(), ExitCode: 1}, nil
			}
			// Destructive-command safety net applies only to non-sandboxed
			// threads — when Bash runs on the host, a stray `rm -rf /path`
			// can nuke real user files. Inside the sandbox the container
			// boundary already scopes destruction to /workspace, and the
			// detector has nothing to add there (it would only reject
			// legitimate sandbox-internal cleanups like `rm -rf build/`).
			if !deps.Sandboxed && sandbox.DetectDestructive(args.Command) {
				return BashResult{Output: "destructive command blocked by safety net (rm / git push / git reset --hard / git checkout --). This is not recoverable from approval — reshape the command (e.g. use a more surgical git operation, or remove files via a safer tool).", ExitCode: 1}, nil
			}
			out, code := execCmd(ctx, args.Command, baseDir)
			fireHook(ctx, "PostToolUse", "Bash")
			return BashResult{Output: out, ExitCode: code}, nil
		},
	)
	if err := addTool("Bash", bash, bashErr); err != nil {
		return nil, err
	}

	fileRead, fileReadErr := functiontool.New(
		functiontool.Config{Name: "FileRead", Description: "Read file contents. Supports offset and limit for large files. On sandboxed threads paths are scoped to the workspace (/workspace)."},
		func(ctx tool.Context, args FileReadArgs) (FileReadResult, error) {
			hostPath, err := resolvePath(args.Path)
			if err != nil {
				return FileReadResult{}, err
			}
			data, err := os.ReadFile(hostPath)
			if err != nil {
				return FileReadResult{}, err
			}
			content := string(data)
			if args.Offset > 0 || args.Limit > 0 {
				lines := strings.Split(content, "\n")
				start := args.Offset
				if start >= len(lines) {
					return FileReadResult{Output: ""}, nil
				}
				end := len(lines)
				if args.Limit > 0 && start+args.Limit < end {
					end = start + args.Limit
				}
				content = strings.Join(lines[start:end], "\n")
			}
			return FileReadResult{Output: content}, nil
		},
	)
	if err := addTool("FileRead", fileRead, fileReadErr); err != nil {
		return nil, err
	}

	glob, globErr := functiontool.New(
		functiontool.Config{Name: "Glob", Description: "Find files matching a glob pattern. On sandboxed threads paths are scoped to the workspace."},
		func(ctx tool.Context, args GlobArgs) (GlobResult, error) {
			dir, err := resolvePath(args.Path)
			if err != nil {
				return GlobResult{}, err
			}
			if dir == "" {
				dir = baseDir
			}
			matches, _ := filepath.Glob(filepath.Join(dir, args.Pattern))
			return GlobResult{Files: matches}, nil
		},
	)
	if err := addTool("Glob", glob, globErr); err != nil { return nil, err }

	grep, grepErr := functiontool.New(
		functiontool.Config{Name: "Grep", Description: "Search file contents for a regex pattern. Returns matching lines with file paths and line numbers. On sandboxed threads paths are scoped to the workspace."},
		func(ctx tool.Context, args GrepArgs) (GrepResult, error) {
			// grep runs through execCmd, which on sandboxed threads
			// invokes the container. Inside the container the only
			// visible tree is /workspace — so we map any model-supplied
			// path into container-relative form. On host-mode the raw
			// or base path is used directly.
			var dir string
			if deps.Sandboxed {
				rel := strings.TrimPrefix(args.Path, sandbox.ContainerWorkspace)
				rel = strings.TrimPrefix(rel, "/")
				// Any absolute host path the model tries to pass gets
				// rejected — if we silently remapped it to /workspace
				// the model would think the scan was wider than it was.
				if filepath.IsAbs(args.Path) && !strings.HasPrefix(args.Path, sandbox.ContainerWorkspace) {
					return GrepResult{}, fmt.Errorf("path %q outside workspace", args.Path)
				}
				if rel == "" {
					dir = sandbox.ContainerWorkspace
				} else {
					dir = sandbox.ContainerWorkspace + "/" + rel
				}
			} else {
				dir = args.Path
				if dir == "" {
					dir = baseDir
				}
			}
			out, _ := execCmd(ctx, fmt.Sprintf("grep -rn %q %s", args.Pattern, dir), "")
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) == 1 && lines[0] == "" {
				lines = nil
			}
			return GrepResult{Matches: lines}, nil
		},
	)
	if err := addTool("Grep", grep, grepErr); err != nil { return nil, err }

	webSearch, webSearchErr := functiontool.New(
		functiontool.Config{Name: "WebSearch", Description: "Search the web for current information. Returns titles, URLs, and snippets."},
		func(ctx tool.Context, args WebSearchArgs) (WebSearchResult, error) {
			if deps.SearchURL == "" {
				return WebSearchResult{}, fmt.Errorf("web search not configured — set a search provider (e.g. SearXNG) in Settings")
			}
			// SearXNG JSON API: GET /search?q=query&format=json
			searchURL := deps.SearchURL + "/search?format=json&q=" + args.Query
			req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
			if err != nil {
				return WebSearchResult{}, err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return WebSearchResult{}, fmt.Errorf("search request: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return WebSearchResult{}, fmt.Errorf("search returned %d", resp.StatusCode)
			}
			var searchResp struct {
				Results []struct {
					Title   string `json:"title"`
					URL     string `json:"url"`
					Content string `json:"content"`
				} `json:"results"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
				return WebSearchResult{}, fmt.Errorf("parse search results: %w", err)
			}
			var results []string
			for _, r := range searchResp.Results {
				if len(results) >= 8 {
					break
				}
				entry := r.Title + " — " + r.URL
				if r.Content != "" {
					entry += "\n" + r.Content
				}
				results = append(results, entry)
			}
			if len(results) == 0 {
				results = []string{"No results found for: " + args.Query}
			}
			return WebSearchResult{Results: results}, nil
		},
	)
	if err := addTool("WebSearch", webSearch, webSearchErr); err != nil { return nil, err }

	webFetch, webFetchErr := functiontool.New(
		functiontool.Config{Name: "WebFetch", Description: "Fetch content from a URL."},
		func(ctx tool.Context, args WebFetchArgs) (WebFetchResult, error) {
			req, err := http.NewRequestWithContext(ctx, "GET", args.URL, nil)
			if err != nil {
				return WebFetchResult{}, err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return WebFetchResult{}, err
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			return WebFetchResult{Output: string(body), StatusCode: resp.StatusCode}, nil
		},
	)
	if err := addTool("WebFetch", webFetch, webFetchErr); err != nil { return nil, err }

	askUser, askUserErr := functiontool.New(
		functiontool.Config{
			Name: "AskUserQuestion",
			Description: "Prompt the user with one or more multiple-choice questions and wait for their answers. " +
				"Use this when you genuinely need a decision from the user that can't be inferred from context.\n\n" +
				"Input: `questions` — a non-null array of 1–4 question objects. Each question MUST have ALL FOUR fields populated:\n" +
				"  • `question` (string): the complete question, ending with a question mark.\n" +
				"  • `header` (string): a short chip label ≤20 chars (e.g. \"Coffee\", \"Library\").\n" +
				"  • `options` (array of 2–4 {label, description}): distinct choices. Do NOT include an \"Other\" option — the UI adds a free-text escape automatically.\n" +
				"  • `multiSelect` (boolean): true when the user may pick multiple options; usually false.\n\n" +
				"Exact shape you must emit:\n" +
				"{\"questions\":[{\"header\":\"Coffee\",\"question\":\"How do you take your coffee?\",\"multiSelect\":false,\"options\":[{\"label\":\"Black\",\"description\":\"No milk, no sugar.\"},{\"label\":\"With milk\",\"description\":\"Splash of milk.\"},{\"label\":\"Sweetened\",\"description\":\"Sugar or syrup added.\"}]}]}\n\n" +
				"DO NOT pass `questions: null` or omit the `options`/`header` fields — the call will error and you must retry with the full structure.\n\n" +
				"Result: `answers` — a map from each question's text to the user's chosen label(s). Multi-select answers are comma-joined. \"Other\" returns the user's typed text.",
		},
		func(ctx tool.Context, args AskUserArgs) (AskUserResult, error) {
			if deps.AskCh == nil {
				return AskUserResult{Answers: map[string]string{}}, nil
			}
			if err := validateAskUserArgs(args); err != nil {
				return AskUserResult{Answers: map[string]string{}}, err
			}
			respCh := make(chan map[string]string, 1)
			deps.AskCh <- AskRequest{Args: args, RespCh: respCh}
			select {
			case <-ctx.Done():
				return AskUserResult{}, ctx.Err()
			case answers := <-respCh:
				return AskUserResult{Answers: answers}, nil
			}
		},
	)
	if err := addTool("AskUser", askUser, askUserErr); err != nil { return nil, err }

	// --- Write tools (Ask) — disabled in plan mode ---

	fileEdit, fileEditErr := functiontool.New(
		functiontool.Config{Name: "FileEdit", Description: "Replace old_string with new_string in the file at path. The old_string must be unique in the file. On sandboxed threads paths are scoped to the workspace."},
		func(ctx tool.Context, args FileEditArgs) (FileEditResult, error) {
			hostPath, err := resolvePath(args.Path)
			if err != nil {
				return FileEditResult{}, err
			}
			if err := planGuard(hostPath); err != nil {
				return FileEditResult{}, err
			}
			if err := fireHook(ctx, "PreToolUse", "FileEdit"); err != nil {
				return FileEditResult{}, err
			}
			argsJSON, _ := json.Marshal(args)
			if err := requireApproval(ctx, "FileEdit", string(argsJSON)); err != nil {
				return FileEditResult{}, err
			}
			data, err := os.ReadFile(hostPath)
			if err != nil {
				return FileEditResult{}, err
			}
			content := string(data)
			if !strings.Contains(content, args.OldString) {
				return FileEditResult{}, fmt.Errorf("old_string not found in file")
			}
			newContent := strings.Replace(content, args.OldString, args.NewString, 1)
			if err := os.WriteFile(hostPath, []byte(newContent), 0o644); err != nil {
				return FileEditResult{}, err
			}
			return FileEditResult{Success: true}, nil
		},
	)
	if err := addTool("FileEdit", fileEdit, fileEditErr); err != nil { return nil, err }

	fileWrite, fileWriteErr := functiontool.New(
		functiontool.Config{Name: "FileWrite", Description: "Write text content to a file, creating parent directories as needed. BOTH `path` AND `content` are required — a call with only `path` will be rejected. On sandboxed threads paths are scoped to the workspace (/workspace); use relative paths like 'novel/ch1.md' or absolute paths like '/workspace/novel/ch1.md'. For large files, emit the full content in a single call; do not split across multiple calls (truncation corrupts the file)."},
		func(ctx tool.Context, args FileWriteArgs) (FileWriteResult, error) {
			hostPath, err := resolvePath(args.Path)
			if err != nil {
				return FileWriteResult{}, err
			}
			if err := planGuard(hostPath); err != nil {
				return FileWriteResult{}, err
			}
			if err := fireHook(ctx, "PreToolUse", "FileWrite"); err != nil {
				return FileWriteResult{}, err
			}
			argsJSON, _ := json.Marshal(args)
			if err := requireApproval(ctx, "FileWrite", string(argsJSON)); err != nil {
				return FileWriteResult{}, err
			}
			if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
				return FileWriteResult{}, err
			}
			if err := os.WriteFile(hostPath, []byte(args.Content), 0o644); err != nil {
				return FileWriteResult{}, err
			}
			return FileWriteResult{Success: true, Path: hostPath}, nil
		},
	)
	if err := addTool("FileWrite", fileWrite, fileWriteErr); err != nil { return nil, err }

	notebookEdit, notebookEditErr := functiontool.New(
		functiontool.Config{Name: "NotebookEdit", Description: "Edit a cell in a Jupyter notebook (.ipynb). Replaces the source content of the specified cell. On sandboxed threads paths are scoped to the workspace."},
		func(ctx tool.Context, args NotebookEditArgs) (NotebookEditResult, error) {
			hostPath, err := resolvePath(args.Path)
			if err != nil {
				return NotebookEditResult{}, err
			}
			if err := planGuard(hostPath); err != nil {
				return NotebookEditResult{}, err
			}
			argsJSON, _ := json.Marshal(args)
			if err := requireApproval(ctx, "NotebookEdit", string(argsJSON)); err != nil {
				return NotebookEditResult{}, err
			}
			data, err := os.ReadFile(hostPath)
			if err != nil {
				return NotebookEditResult{}, err
			}
			var nb map[string]any
			if err := json.Unmarshal(data, &nb); err != nil {
				return NotebookEditResult{}, fmt.Errorf("invalid notebook JSON: %w", err)
			}
			cells, ok := nb["cells"].([]any)
			if !ok {
				return NotebookEditResult{}, fmt.Errorf("notebook has no cells array")
			}
			if args.CellIdx < 0 || args.CellIdx >= len(cells) {
				return NotebookEditResult{}, fmt.Errorf("cell index %d out of range (0-%d)", args.CellIdx, len(cells)-1)
			}
			cell, ok := cells[args.CellIdx].(map[string]any)
			if !ok {
				return NotebookEditResult{}, fmt.Errorf("cell %d is not a valid object", args.CellIdx)
			}
			// Notebook source is an array of lines
			lines := strings.Split(args.Content, "\n")
			sourceLines := make([]any, len(lines))
			for i, l := range lines {
				if i < len(lines)-1 {
					sourceLines[i] = l + "\n"
				} else {
					sourceLines[i] = l
				}
			}
			cell["source"] = sourceLines
			cells[args.CellIdx] = cell
			nb["cells"] = cells
			out, err := json.MarshalIndent(nb, "", " ")
			if err != nil {
				return NotebookEditResult{}, err
			}
			if err := os.WriteFile(hostPath, out, 0o644); err != nil {
				return NotebookEditResult{}, err
			}
			return NotebookEditResult{Success: true}, nil
		},
	)
	if err := addTool("NotebookEdit", notebookEdit, notebookEditErr); err != nil { return nil, err }

	// --- Skill tools (Ask) ---

	skill, skillErr := functiontool.New(
		functiontool.Config{Name: "Skill", Description: "Invoke a skill by name with optional arguments. Skill content is injected as context."},
		func(ctx tool.Context, args SkillArgs) (SkillResult, error) {
			argsJSON, _ := json.Marshal(args)
			if err := requireApproval(ctx, "Skill", string(argsJSON)); err != nil {
				return SkillResult{}, err
			}
			for _, s := range deps.Skills {
				if s.Name == args.Name {
					content := s.Content
					if args.Args != "" {
						content = strings.Replace(content, "$ARGUMENTS", args.Args, -1)
					}
					return SkillResult{Output: content}, nil
				}
			}
			return SkillResult{}, fmt.Errorf("skill '%s' not found", args.Name)
		},
	)
	if err := addTool("Skill", skill, skillErr); err != nil { return nil, err }

	todoWrite, todoWriteErr := functiontool.New(
		functiontool.Config{Name: "TodoWrite", Description: "Create a structured task list for tracking work."},
		func(ctx tool.Context, args TodoWriteArgs) (TodoWriteResult, error) {
			if deps.Tasks == nil {
				return TodoWriteResult{}, fmt.Errorf("task store not available")
			}
			for _, desc := range args.Tasks {
				deps.Tasks.Create(desc, desc, "todo")
			}
			return TodoWriteResult{Success: true}, nil
		},
	)
	if err := addTool("TodoWrite", todoWrite, todoWriteErr); err != nil { return nil, err }

	// --- Plan tools (Ask) ---

	enterPlan, enterPlanErr := functiontool.New(
		functiontool.Config{Name: "EnterPlanMode", Description: "Enter plan mode. Write tools disabled, read tools available. Explore codebase and design implementation approach."},
		func(ctx tool.Context, args PlanModeArgs) (PlanModeResult, error) {
			argsJSON, _ := json.Marshal(args)
			if err := requireApproval(ctx, "EnterPlanMode", string(argsJSON)); err != nil {
				return PlanModeResult{}, err
			}
			if deps.AgentState == nil {
				return PlanModeResult{}, fmt.Errorf("agent state not available")
			}
			if err := deps.AgentState("plan"); err != nil {
				return PlanModeResult{}, err
			}
			// Create plan directory
			if deps.PlanDir != "" {
				slug := fmt.Sprintf("plan-%s", deps.ThreadID)
				planPath := filepath.Join(deps.PlanDir, slug)
				os.MkdirAll(planPath, 0o755)
			}
			return PlanModeResult{Success: true}, nil
		},
	)
	if err := addTool("EnterPlan", enterPlan, enterPlanErr); err != nil { return nil, err }

	exitPlan, exitPlanErr := functiontool.New(
		functiontool.Config{Name: "ExitPlanMode", Description: "Signal that the plan is ready for user review. Do NOT paraphrase or re-summarize the plan — the user sees it surfaced in the artifacts panel. A brief one-line acknowledgment is enough."},
		func(ctx tool.Context, args PlanModeArgs) (PlanModeResult, error) {
			argsJSON, _ := json.Marshal(args)
			if err := requireApproval(ctx, "ExitPlanMode", string(argsJSON)); err != nil {
				return PlanModeResult{}, err
			}
			// Compile plan from adoc artifact if it exists
			var planContent string
			if deps.CompileAdoc != nil && deps.PlanDir != "" {
				slug := fmt.Sprintf("plan-%s", deps.ThreadID)
				planEntry := filepath.Join(deps.PlanDir, slug, "plan.adoc")
				if compiled, err := deps.CompileAdoc(planEntry); err == nil && compiled != "" {
					planContent = compiled
				}
			}
			// Surface plan content to frontend.
			// Note: mode stays Plan. The user explicitly approves/rejects via
			// approvePlan/rejectPlan mutations — that's when mode flips (or
			// stays Plan and iterates, if the user just sends more feedback).
			// Previously this tool flipped to Normal immediately, which meant
			// "no action = keep iterating" was impossible (mode was gone).
			if deps.OnPlanContent != nil && planContent != "" {
				deps.OnPlanContent(planContent)
			}
			return PlanModeResult{Success: true}, nil
		},
	)
	if err := addTool("ExitPlan", exitPlan, exitPlanErr); err != nil { return nil, err }

	// --- Agent tools (Ask) ---

	agentTool, agentToolErr := functiontool.New(
		functiontool.Config{Name: "Agent", Description: "Spawn a subagent to handle a complex task autonomously. The subagent runs in an ephemeral thread fork with its own RRC state."},
		func(ctx tool.Context, args AgentToolArgs) (AgentToolResult, error) {
			argsJSON, _ := json.Marshal(args)
			if err := requireApproval(ctx, "Agent", string(argsJSON)); err != nil {
				return AgentToolResult{}, err
			}
			if deps.SpawnAgent == nil {
				return AgentToolResult{}, fmt.Errorf("subagent spawning not available")
			}
			forkID := fmt.Sprintf("fork-%s-%d", deps.ThreadID, deps.Tasks.seq.Load())
			result, err := deps.SpawnAgent(ctx, args.Task, forkID)
			if err != nil {
				return AgentToolResult{}, err
			}
			return AgentToolResult{Result: result}, nil
		},
	)
	if err := addTool("Agent", agentTool, agentToolErr); err != nil { return nil, err }

	sendMsg, sendMsgErr := functiontool.New(
		functiontool.Config{Name: "SendMessage", Description: "Send a message to a running subagent. The subagent receives it as a new user message in its forked thread."},
		func(ctx tool.Context, args SendMessageArgs) (SendMessageResult, error) {
			argsJSON, _ := json.Marshal(args)
			if err := requireApproval(ctx, "SendMessage", string(argsJSON)); err != nil {
				return SendMessageResult{}, err
			}
			if deps.SendToAgent == nil {
				return SendMessageResult{}, fmt.Errorf("agent messaging not available")
			}
			resp, err := deps.SendToAgent(ctx, args.To, args.Message)
			if err != nil {
				return SendMessageResult{}, err
			}
			return SendMessageResult{Response: resp}, nil
		},
	)
	if err := addTool("SendMessage", sendMsg, sendMsgErr); err != nil { return nil, err }

	taskCreate, taskCreateErr := functiontool.New(
		functiontool.Config{Name: "TaskCreate", Description: "Create a task to track work progress. Returns the task ID."},
		func(ctx tool.Context, args TaskCreateArgs) (TaskCreateResult, error) {
			if deps.Tasks == nil {
				return TaskCreateResult{}, fmt.Errorf("task store not initialized")
			}
			t := deps.Tasks.Create(args.Subject, args.Description, "")
			return TaskCreateResult{TaskID: t.ID}, nil
		},
	)
	if err := addTool("TaskCreate", taskCreate, taskCreateErr); err != nil { return nil, err }

	taskGet, taskGetErr := functiontool.New(
		functiontool.Config{Name: "TaskGet", Description: "Get details of a specific task by ID."},
		func(ctx tool.Context, args TaskGetArgs) (TaskGetResult, error) {
			if deps.Tasks == nil {
				return TaskGetResult{}, fmt.Errorf("task store not initialized")
			}
			t := deps.Tasks.Get(args.TaskID)
			if t == nil {
				return TaskGetResult{}, fmt.Errorf("task %s not found", args.TaskID)
			}
			return TaskGetResult{Subject: t.Subject, Status: t.Status}, nil
		},
	)
	if err := addTool("TaskGet", taskGet, taskGetErr); err != nil { return nil, err }

	taskUpdate, taskUpdateErr := functiontool.New(
		functiontool.Config{Name: "TaskUpdate", Description: "Update a task's status (pending, in_progress, completed) or delete it (status=deleted)."},
		func(ctx tool.Context, args TaskUpdateArgs) (TaskUpdateResult, error) {
			if deps.Tasks == nil {
				return TaskUpdateResult{}, fmt.Errorf("task store not initialized")
			}
			if args.Status == "deleted" {
				deps.Tasks.Delete(args.TaskID)
				return TaskUpdateResult{Success: true}, nil
			}
			ok := deps.Tasks.Update(args.TaskID, args.Status, "", "", "")
			if !ok {
				return TaskUpdateResult{}, fmt.Errorf("task %s not found", args.TaskID)
			}
			return TaskUpdateResult{Success: true}, nil
		},
	)
	if err := addTool("TaskUpdate", taskUpdate, taskUpdateErr); err != nil { return nil, err }

	taskList, taskListErr := functiontool.New(
		functiontool.Config{Name: "TaskList", Description: "List all tasks with their status."},
		func(ctx tool.Context, args TaskListArgs) (TaskListResult, error) {
			if deps.Tasks == nil {
				return TaskListResult{Tasks: []string{}}, nil
			}
			tasks := deps.Tasks.List()
			lines := make([]string, len(tasks))
			for i, t := range tasks {
				lines[i] = fmt.Sprintf("#%s [%s] %s", t.ID, t.Status, t.Subject)
			}
			return TaskListResult{Tasks: lines}, nil
		},
	)
	if err := addTool("TaskList", taskList, taskListErr); err != nil { return nil, err }

	taskStop, taskStopErr := functiontool.New(
		functiontool.Config{Name: "TaskStop", Description: "Stop a running task by marking it completed."},
		func(ctx tool.Context, args TaskStopArgs) (TaskStopResult, error) {
			if deps.Tasks == nil {
				return TaskStopResult{}, fmt.Errorf("task store not initialized")
			}
			ok := deps.Tasks.Update(args.TaskID, "completed", "", "", "")
			return TaskStopResult{Success: ok}, nil
		},
	)
	if err := addTool("TaskStop", taskStop, taskStopErr); err != nil { return nil, err }

	taskOutput, taskOutputErr := functiontool.New(
		functiontool.Config{Name: "TaskOutput", Description: "Get the description and status of a task."},
		func(ctx tool.Context, args TaskOutputArgs) (TaskOutputResult, error) {
			if deps.Tasks == nil {
				return TaskOutputResult{}, fmt.Errorf("task store not initialized")
			}
			t := deps.Tasks.Get(args.TaskID)
			if t == nil {
				return TaskOutputResult{}, fmt.Errorf("task %s not found", args.TaskID)
			}
			return TaskOutputResult{Output: fmt.Sprintf("[%s] %s: %s", t.Status, t.Subject, t.Description)}, nil
		},
	)
	if err := addTool("TaskOutput", taskOutput, taskOutputErr); err != nil { return nil, err }

	return tools, nil
}

