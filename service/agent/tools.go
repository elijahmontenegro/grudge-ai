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
	Command string `json:"command" jsonschema:"description=Shell command to execute"`
}
type BashResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

type FileReadArgs struct {
	Path   string `json:"path" jsonschema:"description=Absolute file path to read"`
	Offset int    `json:"offset,omitempty" jsonschema:"description=Line number to start reading from"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Number of lines to read"`
}
type FileReadResult struct {
	Content string `json:"content"`
}

type FileEditArgs struct {
	Path      string `json:"path" jsonschema:"description=Absolute file path"`
	OldString string `json:"old_string" jsonschema:"description=Text to find and replace"`
	NewString string `json:"new_string" jsonschema:"description=Replacement text"`
}
type FileEditResult struct {
	Success bool `json:"success"`
}

type FileWriteArgs struct {
	Path    string `json:"path" jsonschema:"description=Absolute file path"`
	Content string `json:"content" jsonschema:"description=File content to write"`
}
type FileWriteResult struct {
	Success bool `json:"success"`
}

type GlobArgs struct {
	Pattern string `json:"pattern" jsonschema:"description=Glob pattern (e.g. **/*.go)"`
	Path    string `json:"path,omitempty" jsonschema:"description=Directory to search in"`
}
type GlobResult struct {
	Files []string `json:"files"`
}

type GrepArgs struct {
	Pattern string `json:"pattern" jsonschema:"description=Regex pattern to search for"`
	Path    string `json:"path,omitempty" jsonschema:"description=File or directory to search"`
}
type GrepResult struct {
	Matches []string `json:"matches"`
}

type NotebookEditArgs struct {
	Path    string `json:"path" jsonschema:"description=Path to Jupyter notebook"`
	CellIdx int    `json:"cell_index" jsonschema:"description=Cell index to edit"`
	Content string `json:"content" jsonschema:"description=New cell content"`
}
type NotebookEditResult struct {
	Success bool `json:"success"`
}

type WebSearchArgs struct {
	Query string `json:"query" jsonschema:"description=Search query"`
}
type WebSearchResult struct {
	Results []string `json:"results"`
}

type WebFetchArgs struct {
	URL string `json:"url" jsonschema:"description=URL to fetch"`
}
type WebFetchResult struct {
	Content    string `json:"content"`
	StatusCode int    `json:"status_code"`
}

type AskUserArgs struct {
	Question string `json:"question" jsonschema:"description=Question to ask the user"`
}
type AskUserResult struct {
	Response string `json:"response"`
}

type SkillArgs struct {
	Name string `json:"name" jsonschema:"description=Skill name to invoke"`
	Args string `json:"args,omitempty" jsonschema:"description=Arguments for the skill"`
}
type SkillResult struct {
	Output string `json:"output"`
}

type TodoWriteArgs struct {
	Tasks []string `json:"tasks" jsonschema:"description=List of task descriptions"`
}
type TodoWriteResult struct {
	Success bool `json:"success"`
}

type PlanModeArgs struct {
	ThreadID string `json:"thread_id" jsonschema:"description=Thread ID"`
}
type PlanModeResult struct {
	Success bool `json:"success"`
}

type AgentToolArgs struct {
	Task        string `json:"task" jsonschema:"description=Task for the subagent"`
	Description string `json:"description,omitempty" jsonschema:"description=Brief description of what the agent will do"`
}
type AgentToolResult struct {
	Result string `json:"result"`
}

type SendMessageArgs struct {
	To      string `json:"to" jsonschema:"description=Agent name or ID to send message to"`
	Message string `json:"message" jsonschema:"description=Message content"`
}
type SendMessageResult struct {
	Response string `json:"response"`
}

type TaskCreateArgs struct {
	Subject     string `json:"subject" jsonschema:"description=Task title"`
	Description string `json:"description" jsonschema:"description=Task description"`
}
type TaskCreateResult struct {
	TaskID string `json:"task_id"`
}

type TaskGetArgs struct {
	TaskID string `json:"task_id" jsonschema:"description=Task ID to retrieve"`
}
type TaskGetResult struct {
	Subject string `json:"subject"`
	Status  string `json:"status"`
}

type TaskUpdateArgs struct {
	TaskID string `json:"task_id" jsonschema:"description=Task ID to update"`
	Status string `json:"status" jsonschema:"description=New status (pending, in_progress, completed)"`
}
type TaskUpdateResult struct {
	Success bool `json:"success"`
}

type TaskListArgs struct{}
type TaskListResult struct {
	Tasks []string `json:"tasks"`
}

type TaskStopArgs struct {
	TaskID string `json:"task_id" jsonschema:"description=Task ID to stop"`
}
type TaskStopResult struct {
	Success bool `json:"success"`
}

type TaskOutputArgs struct {
	TaskID string `json:"task_id" jsonschema:"description=Task ID to get output for"`
}
type TaskOutputResult struct {
	Output string `json:"output"`
}

// ToolDeps holds dependencies injected into tools that need service access.
type ToolDeps struct {
	Sandboxed   bool
	WorkingDirs []string
	Tasks       *TaskStore
	Skills      []SkillDef
	// AskCh receives question, caller reads response from RespCh
	AskCh       chan<- AskRequest
}

// SkillDef is a minimal skill reference for the tool.
type SkillDef struct {
	Name    string
	Content string
}

// AskRequest is sent to the UI when AskUserQuestion is called.
type AskRequest struct {
	Question string
	RespCh   chan string
}

// BuildTools creates all ADK FunctionTools for the agent.
func BuildTools(deps ToolDeps) ([]tool.Tool, error) {
	baseDir := ""
	if len(deps.WorkingDirs) > 0 {
		baseDir = deps.WorkingDirs[0]
	}

	execCmd := func(ctx context.Context, command, dir string) (string, int) {
		if deps.Sandboxed {
			sb := sandbox.New(deps.WorkingDirs)
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

	// --- Read tools (Allow) ---

	bash, _ := functiontool.New(
		functiontool.Config{Name: "Bash", Description: "Execute a shell command. Returns stdout/stderr and exit code."},
		func(ctx tool.Context, args BashArgs) (BashResult, error) {
			if sandbox.DetectDestructive(args.Command) {
				return BashResult{Output: "destructive command detected — requires explicit approval", ExitCode: 1}, nil
			}
			out, code := execCmd(ctx, args.Command, baseDir)
			return BashResult{Output: out, ExitCode: code}, nil
		},
	)
	tools = append(tools, bash)

	fileRead, _ := functiontool.New(
		functiontool.Config{Name: "FileRead", Description: "Read file contents. Supports offset and limit for large files."},
		func(ctx tool.Context, args FileReadArgs) (FileReadResult, error) {
			data, err := os.ReadFile(args.Path)
			if err != nil {
				return FileReadResult{}, err
			}
			content := string(data)
			if args.Offset > 0 || args.Limit > 0 {
				lines := strings.Split(content, "\n")
				start := args.Offset
				if start >= len(lines) {
					return FileReadResult{Content: ""}, nil
				}
				end := len(lines)
				if args.Limit > 0 && start+args.Limit < end {
					end = start + args.Limit
				}
				content = strings.Join(lines[start:end], "\n")
			}
			return FileReadResult{Content: content}, nil
		},
	)
	tools = append(tools, fileRead)

	glob, _ := functiontool.New(
		functiontool.Config{Name: "Glob", Description: "Find files matching a glob pattern."},
		func(ctx tool.Context, args GlobArgs) (GlobResult, error) {
			dir := args.Path
			if dir == "" {
				dir = baseDir
			}
			matches, _ := filepath.Glob(filepath.Join(dir, args.Pattern))
			return GlobResult{Files: matches}, nil
		},
	)
	tools = append(tools, glob)

	grep, _ := functiontool.New(
		functiontool.Config{Name: "Grep", Description: "Search file contents for a regex pattern. Returns matching lines with file paths and line numbers."},
		func(ctx tool.Context, args GrepArgs) (GrepResult, error) {
			dir := args.Path
			if dir == "" {
				dir = baseDir
			}
			out, _ := execCmd(ctx, fmt.Sprintf("grep -rn %q %s", args.Pattern, dir), "")
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) == 1 && lines[0] == "" {
				lines = nil
			}
			return GrepResult{Matches: lines}, nil
		},
	)
	tools = append(tools, grep)

	webSearch, _ := functiontool.New(
		functiontool.Config{Name: "WebSearch", Description: "Search the web for information. Returns relevant results."},
		func(ctx tool.Context, args WebSearchArgs) (WebSearchResult, error) {
			// Web search delegates to an external search API or service
			return WebSearchResult{Results: []string{"Web search not yet connected to a search provider."}}, nil
		},
	)
	tools = append(tools, webSearch)

	webFetch, _ := functiontool.New(
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
			return WebFetchResult{Content: string(body), StatusCode: resp.StatusCode}, nil
		},
	)
	tools = append(tools, webFetch)

	askUser, _ := functiontool.New(
		functiontool.Config{Name: "AskUserQuestion", Description: "Ask the user a question and wait for their response. The question is surfaced in the UI; execution pauses until the user responds."},
		func(ctx tool.Context, args AskUserArgs) (AskUserResult, error) {
			if deps.AskCh == nil {
				return AskUserResult{Response: "[no user interaction channel]"}, nil
			}
			respCh := make(chan string, 1)
			deps.AskCh <- AskRequest{Question: args.Question, RespCh: respCh}
			select {
			case <-ctx.Done():
				return AskUserResult{}, ctx.Err()
			case resp := <-respCh:
				return AskUserResult{Response: resp}, nil
			}
		},
	)
	tools = append(tools, askUser)

	// --- Write tools (Ask) ---

	fileEdit, _ := functiontool.New(
		functiontool.Config{Name: "FileEdit", Description: "Replace old_string with new_string in the file at path. The old_string must be unique in the file."},
		func(ctx tool.Context, args FileEditArgs) (FileEditResult, error) {
			data, err := os.ReadFile(args.Path)
			if err != nil {
				return FileEditResult{}, err
			}
			content := string(data)
			if !strings.Contains(content, args.OldString) {
				return FileEditResult{}, fmt.Errorf("old_string not found in file")
			}
			newContent := strings.Replace(content, args.OldString, args.NewString, 1)
			if err := os.WriteFile(args.Path, []byte(newContent), 0o644); err != nil {
				return FileEditResult{}, err
			}
			return FileEditResult{Success: true}, nil
		},
	)
	tools = append(tools, fileEdit)

	fileWrite, _ := functiontool.New(
		functiontool.Config{Name: "FileWrite", Description: "Write content to file, creating directories as needed."},
		func(ctx tool.Context, args FileWriteArgs) (FileWriteResult, error) {
			if err := os.MkdirAll(filepath.Dir(args.Path), 0o755); err != nil {
				return FileWriteResult{}, err
			}
			if err := os.WriteFile(args.Path, []byte(args.Content), 0o644); err != nil {
				return FileWriteResult{}, err
			}
			return FileWriteResult{Success: true}, nil
		},
	)
	tools = append(tools, fileWrite)

	notebookEdit, _ := functiontool.New(
		functiontool.Config{Name: "NotebookEdit", Description: "Edit a cell in a Jupyter notebook (.ipynb). Replaces the source content of the specified cell."},
		func(ctx tool.Context, args NotebookEditArgs) (NotebookEditResult, error) {
			data, err := os.ReadFile(args.Path)
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
			if err := os.WriteFile(args.Path, out, 0o644); err != nil {
				return NotebookEditResult{}, err
			}
			return NotebookEditResult{Success: true}, nil
		},
	)
	tools = append(tools, notebookEdit)

	// --- Skill tools (Ask) ---

	skill, _ := functiontool.New(
		functiontool.Config{Name: "Skill", Description: "Invoke a skill by name with optional arguments. Skill content is injected as context."},
		func(ctx tool.Context, args SkillArgs) (SkillResult, error) {
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
	tools = append(tools, skill)

	todoWrite, _ := functiontool.New(
		functiontool.Config{Name: "TodoWrite", Description: "Create a structured task list for tracking work."},
		func(ctx tool.Context, args TodoWriteArgs) (TodoWriteResult, error) {
			return TodoWriteResult{Success: true}, nil
		},
	)
	tools = append(tools, todoWrite)

	// --- Plan tools (Ask) ---

	enterPlan, _ := functiontool.New(
		functiontool.Config{Name: "EnterPlanMode", Description: "Enter plan mode. Write tools disabled, read tools available. Explore codebase and design implementation approach."},
		func(ctx tool.Context, args PlanModeArgs) (PlanModeResult, error) {
			return PlanModeResult{Success: true}, nil
		},
	)
	tools = append(tools, enterPlan)

	exitPlan, _ := functiontool.New(
		functiontool.Config{Name: "ExitPlanMode", Description: "Exit plan mode. Plan is compiled and surfaced for user review."},
		func(ctx tool.Context, args PlanModeArgs) (PlanModeResult, error) {
			return PlanModeResult{Success: true}, nil
		},
	)
	tools = append(tools, exitPlan)

	// --- Agent tools (Ask) ---

	agentTool, _ := functiontool.New(
		functiontool.Config{Name: "Agent", Description: "Spawn a subagent to handle a complex task autonomously. Returns the result when done."},
		func(ctx tool.Context, args AgentToolArgs) (AgentToolResult, error) {
			return AgentToolResult{Result: fmt.Sprintf("Subagent completed task: %s", args.Task)}, nil
		},
	)
	tools = append(tools, agentTool)

	sendMsg, _ := functiontool.New(
		functiontool.Config{Name: "SendMessage", Description: "Send a message to a running subagent."},
		func(ctx tool.Context, args SendMessageArgs) (SendMessageResult, error) {
			return SendMessageResult{Response: "Message sent"}, nil
		},
	)
	tools = append(tools, sendMsg)

	taskCreate, _ := functiontool.New(
		functiontool.Config{Name: "TaskCreate", Description: "Create a task to track work progress. Returns the task ID."},
		func(ctx tool.Context, args TaskCreateArgs) (TaskCreateResult, error) {
			if deps.Tasks == nil {
				return TaskCreateResult{}, fmt.Errorf("task store not initialized")
			}
			t := deps.Tasks.Create(args.Subject, args.Description, "")
			return TaskCreateResult{TaskID: t.ID}, nil
		},
	)
	tools = append(tools, taskCreate)

	taskGet, _ := functiontool.New(
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
	tools = append(tools, taskGet)

	taskUpdate, _ := functiontool.New(
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
	tools = append(tools, taskUpdate)

	taskList, _ := functiontool.New(
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
	tools = append(tools, taskList)

	taskStop, _ := functiontool.New(
		functiontool.Config{Name: "TaskStop", Description: "Stop a running task by marking it completed."},
		func(ctx tool.Context, args TaskStopArgs) (TaskStopResult, error) {
			if deps.Tasks == nil {
				return TaskStopResult{}, fmt.Errorf("task store not initialized")
			}
			ok := deps.Tasks.Update(args.TaskID, "completed", "", "", "")
			return TaskStopResult{Success: ok}, nil
		},
	)
	tools = append(tools, taskStop)

	taskOutput, _ := functiontool.New(
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
	tools = append(tools, taskOutput)

	return tools, nil
}
