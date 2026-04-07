package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// Tool categories and their default permission levels.
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
	Path string `json:"path" jsonschema:"description=Absolute file path to read"`
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

// BuildTools creates all ADK FunctionTools for the agent.
func BuildTools(workingDirs []string) ([]tool.Tool, error) {
	baseDir := ""
	if len(workingDirs) > 0 {
		baseDir = workingDirs[0]
	}

	bash, err := functiontool.New(
		functiontool.Config{Name: "Bash", Description: "Execute a shell command and return output"},
		func(ctx tool.Context, args BashArgs) (BashResult, error) {
			cmd := exec.CommandContext(ctx, "sh", "-c", args.Command)
			if baseDir != "" {
				cmd.Dir = baseDir
			}
			out, err := cmd.CombinedOutput()
			exitCode := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				}
			}
			return BashResult{Output: string(out), ExitCode: exitCode}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("bash tool: %w", err)
	}

	fileRead, err := functiontool.New(
		functiontool.Config{Name: "FileRead", Description: "Read the contents of a file at the given path"},
		func(ctx tool.Context, args FileReadArgs) (FileReadResult, error) {
			data, err := os.ReadFile(args.Path)
			if err != nil {
				return FileReadResult{}, err
			}
			return FileReadResult{Content: string(data)}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("file_read tool: %w", err)
	}

	fileEdit, err := functiontool.New(
		functiontool.Config{Name: "FileEdit", Description: "Replace old_string with new_string in the file at path"},
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
	if err != nil {
		return nil, fmt.Errorf("file_edit tool: %w", err)
	}

	fileWrite, err := functiontool.New(
		functiontool.Config{Name: "FileWrite", Description: "Write content to the file at path, creating directories as needed"},
		func(ctx tool.Context, args FileWriteArgs) (FileWriteResult, error) {
			dir := filepath.Dir(args.Path)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return FileWriteResult{}, err
			}
			if err := os.WriteFile(args.Path, []byte(args.Content), 0o644); err != nil {
				return FileWriteResult{}, err
			}
			return FileWriteResult{Success: true}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("file_write tool: %w", err)
	}

	glob, err := functiontool.New(
		functiontool.Config{Name: "Glob", Description: "Find files matching a glob pattern"},
		func(ctx tool.Context, args GlobArgs) (GlobResult, error) {
			dir := args.Path
			if dir == "" {
				dir = baseDir
			}
			pattern := filepath.Join(dir, args.Pattern)
			matches, err := filepath.Glob(pattern)
			if err != nil {
				return GlobResult{}, err
			}
			return GlobResult{Files: matches}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("glob tool: %w", err)
	}

	grep, err := functiontool.New(
		functiontool.Config{Name: "Grep", Description: "Search file contents for a regex pattern"},
		func(ctx tool.Context, args GrepArgs) (GrepResult, error) {
			dir := args.Path
			if dir == "" {
				dir = baseDir
			}
			cmd := exec.CommandContext(ctx, "grep", "-rn", args.Pattern, dir)
			out, _ := cmd.CombinedOutput()
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) == 1 && lines[0] == "" {
				lines = nil
			}
			return GrepResult{Matches: lines}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("grep tool: %w", err)
	}

	return []tool.Tool{bash, fileRead, fileEdit, fileWrite, glob, grep}, nil
}
