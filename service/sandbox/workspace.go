package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emontenegr/spidey/service/storage"
)

// ContainerWorkspace is the path at which the host workspace directory
// is bind-mounted inside the sandbox container. Exposed as a constant
// so the system prompt, the Go path guard, and the docker invocation
// all agree on the name the model should use.
const ContainerWorkspace = "/workspace"

// WorkspaceDir returns the absolute sandbox workspace directory for a
// thread and ensures it exists. Rejects malformed thread IDs so a
// client-supplied value can't traverse outside the sandboxes root.
//
// Layout mirrors the plan directories:
//
//	{DataDir}/sandboxes/sbx-{threadID}/
//	{DataDir}/plans/plan-{threadID}/
func WorkspaceDir(dataDir, threadID string) (string, error) {
	if err := storage.ValidateThreadID(threadID); err != nil {
		return "", err
	}
	root, err := filepath.Abs(filepath.Join(dataDir, "sandboxes"))
	if err != nil {
		return "", fmt.Errorf("resolve sandboxes root: %w", err)
	}
	dir := filepath.Clean(filepath.Join(root, "sbx-"+threadID))
	// Belt-and-suspenders: filepath.Clean on a malformed ID could in
	// principle land back inside root, so verify the final path is
	// under root.
	if dir != root && !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid threadID: path escapes sandboxes directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}
	return dir, nil
}

// ResolveWorkspacePath maps a path supplied by the model into an
// absolute host path, verifying it stays inside the workspace. Used by
// every file tool (FileRead/FileWrite/FileEdit/NotebookEdit) on
// sandboxed threads so prompt-injected paths like "/etc/passwd" or
// "C:\Users\...\.ssh\id_rsa" cannot reach the host.
//
// Accepts:
//   - relative paths           → joined against workspace
//   - /workspace/... (container view) → mapped to workspace root
//   - host-absolute paths already under the workspace → returned as-is
//
// Rejects any path whose cleaned form escapes the workspace, and
// refuses empty workspaces so a missing wiring can't silently degrade
// to the old unsafe behavior.
func ResolveWorkspacePath(workspace, path string) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("sandbox workspace not configured")
	}
	absWs, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	if path == "" {
		return absWs, nil
	}
	if path == ContainerWorkspace {
		return absWs, nil
	}
	if strings.HasPrefix(path, ContainerWorkspace+"/") {
		rel := strings.TrimPrefix(path, ContainerWorkspace+"/")
		return safeJoin(absWs, rel)
	}
	if filepath.IsAbs(path) {
		clean := filepath.Clean(path)
		if clean == absWs || strings.HasPrefix(clean, absWs+string(os.PathSeparator)) {
			return clean, nil
		}
		return "", fmt.Errorf("path %q escapes workspace %s", path, absWs)
	}
	return safeJoin(absWs, path)
}

func safeJoin(root, rel string) (string, error) {
	joined := filepath.Clean(filepath.Join(root, rel))
	if joined != root && !strings.HasPrefix(joined, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes workspace %s", rel, root)
	}
	return joined, nil
}
