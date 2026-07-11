// Package datadir is the single owner of the application's data-dir
// layout. Every path anchored on {DataDir} — sandbox workspaces, plan
// directories, attachment roots, the calibrator artifact, the skills
// directory — is computed here, so the layout has one definition and
// the thread-ID path-traversal defense lives in one place.
//
// Layout:
//
//	{DataDir}/sandboxes/sbx-{threadID}/               workspace (mounted at /workspace)
//	{DataDir}/sandboxes/sbx-{threadID}/_attachments/  attachment store
//	{DataDir}/plans/plan-{threadID}/                  plan documents
//	{DataDir}/calibrator.json                         fitted acceptance calibrator
//	{DataDir}/tokenscale.json                         learned per-model token scales
//	{DataDir}/skills/                                 user skills
package datadir

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ThreadIDPattern enforces the server-generated thread ID format
// (`thread-{unixnano}`). Every callsite that interpolates a
// client-supplied thread ID into a filesystem path runs it through
// ValidateThreadID first. Refusing anything else at the boundary
// prevents both hard traversal (../etc/foo) and soft traversal whose
// Clean'd result stays inside the root directory but outside the
// thread-{id} convention.
var ThreadIDPattern = regexp.MustCompile(`^thread-\d+$`)

// ValidateThreadID returns nil iff id matches the server-generated
// thread ID format. Returns a descriptive error otherwise.
func ValidateThreadID(id string) error {
	if id == "" {
		return fmt.Errorf("empty threadID")
	}
	if !ThreadIDPattern.MatchString(id) {
		return fmt.Errorf("invalid threadID format: %q", id)
	}
	return nil
}

// WorkspaceDir returns the absolute sandbox workspace directory for a
// thread and ensures it exists. Rejects malformed thread IDs so a
// client-supplied value can't traverse outside the sandboxes root.
func WorkspaceDir(dataDir, threadID string) (string, error) {
	dir, err := threadScopedDir(dataDir, "sandboxes", "sbx-", threadID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}
	return dir, nil
}

// PlanDirForThread returns the absolute plan directory for a thread.
// Does not create it — plan lifecycle owns creation.
func PlanDirForThread(dataDir, threadID string) (string, error) {
	return threadScopedDir(dataDir, "plans", "plan-", threadID)
}

// AttachmentsDir returns the absolute attachment root for a thread —
// inside the sandbox workspace, so attachments are immediately visible
// at /workspace/_attachments in sandboxed threads. Does not create it.
func AttachmentsDir(dataDir, threadID string) (string, error) {
	ws, err := threadScopedDir(dataDir, "sandboxes", "sbx-", threadID)
	if err != nil {
		return "", err
	}
	return filepath.Join(ws, "_attachments"), nil
}

// TokenScalePath is the learned per-model token-scale artifact,
// written by rrc/tokenscale as completions report usage and loaded at
// boot.
func TokenScalePath(dataDir string) string {
	return filepath.Join(dataDir, "tokenscale.json")
}

// SkillsDir is where user skills are loaded from.
func SkillsDir(dataDir string) string {
	return filepath.Join(dataDir, "skills")
}

// threadScopedDir computes {dataDir}/{root}/{prefix}{threadID} with the
// full traversal defense: format validation plus a belt-and-suspenders
// containment check in case the ID format ever loosens.
func threadScopedDir(dataDir, root, prefix, threadID string) (string, error) {
	if err := ValidateThreadID(threadID); err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(filepath.Join(dataDir, root))
	if err != nil {
		return "", fmt.Errorf("resolve %s root: %w", root, err)
	}
	dir := filepath.Clean(filepath.Join(absRoot, prefix+threadID))
	if dir != absRoot && !strings.HasPrefix(dir, absRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid threadID: path escapes %s directory", root)
	}
	return dir, nil
}
