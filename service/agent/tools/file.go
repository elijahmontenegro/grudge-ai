package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// File tools — FileRead is read-side (Allow); FileEdit, FileWrite,
// NotebookEdit are write-side (Ask, plan-mode-disabled).
//
// FileRead lives here because its rendering shape (offset/limit line
// slicing) belongs with the file family even though its permission
// matches the read tools.

func registerFileReadTool(c *buildCtx) error {
	fileRead, err := functiontool.New(
		functiontool.Config{Name: "FileRead", Description: "Read file contents. Supports offset and limit for large files. On sandboxed threads paths are scoped to the workspace (/workspace)."},
		func(ctx tool.Context, args FileReadArgs) (FileReadResult, error) {
			hostPath, err := c.resolvePath(args.Path)
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
	return c.addTool("FileRead", fileRead, err)
}

func registerWriteTools(c *buildCtx) error {
	fileEdit, err := functiontool.New(
		functiontool.Config{Name: "FileEdit", Description: "Replace old_string with new_string in the file at path. The old_string must be unique in the file. On sandboxed threads paths are scoped to the workspace."},
		func(ctx tool.Context, args FileEditArgs) (FileEditResult, error) {
			hostPath, err := c.resolvePath(args.Path)
			if err != nil {
				return FileEditResult{}, err
			}
			if err := c.planGuard(hostPath); err != nil {
				return FileEditResult{}, err
			}
			if err := c.fireHook(ctx, "PreToolUse", "FileEdit"); err != nil {
				return FileEditResult{}, err
			}
			if err := c.requireApproval(ctx, "FileEdit", marshalArgs(args)); err != nil {
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
	if err := c.addTool("FileEdit", fileEdit, err); err != nil {
		return err
	}

	fileWrite, err := functiontool.New(
		functiontool.Config{Name: "FileWrite", Description: "Write text content to a file, creating parent directories as needed. BOTH `path` AND `content` are required — a call with only `path` will be rejected. On sandboxed threads paths are scoped to the workspace (/workspace); use relative paths like 'novel/ch1.md' or absolute paths like '/workspace/novel/ch1.md'. For large files, emit the full content in a single call; do not split across multiple calls (truncation corrupts the file)."},
		func(ctx tool.Context, args FileWriteArgs) (FileWriteResult, error) {
			hostPath, err := c.resolvePath(args.Path)
			if err != nil {
				return FileWriteResult{}, err
			}
			if err := c.planGuard(hostPath); err != nil {
				return FileWriteResult{}, err
			}
			if err := c.fireHook(ctx, "PreToolUse", "FileWrite"); err != nil {
				return FileWriteResult{}, err
			}
			if err := c.requireApproval(ctx, "FileWrite", marshalArgs(args)); err != nil {
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
	if err := c.addTool("FileWrite", fileWrite, err); err != nil {
		return err
	}

	notebookEdit, err := functiontool.New(
		functiontool.Config{Name: "NotebookEdit", Description: "Edit a cell in a Jupyter notebook (.ipynb). Replaces the source content of the specified cell. On sandboxed threads paths are scoped to the workspace."},
		func(ctx tool.Context, args NotebookEditArgs) (NotebookEditResult, error) {
			hostPath, err := c.resolvePath(args.Path)
			if err != nil {
				return NotebookEditResult{}, err
			}
			if err := c.planGuard(hostPath); err != nil {
				return NotebookEditResult{}, err
			}
			if err := c.requireApproval(ctx, "NotebookEdit", marshalArgs(args)); err != nil {
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
	return c.addTool("NotebookEdit", notebookEdit, err)
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
