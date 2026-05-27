package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Filesystem-path concerns anchored on thread IDs. The regex, the
// validation entry point, and the path computers share one home so
// the path-traversal defense lives in one place.
//
// CreateThread emits IDs of the form `thread-{unixnano}`; every
// callsite that interpolates a client-supplied thread ID into a
// filesystem path runs it through ValidateThreadID first. Refusing
// anything else at the boundary prevents both hard traversal
// (../etc/foo) and soft traversal whose Clean'd result stays inside
// the root directory but outside the thread-{id} convention.

// ThreadIDPattern enforces the server-generated thread ID format.
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

// PlanDirForThread returns the absolute plan directory for a
// thread. Layout mirrors sandbox.WorkspaceDir:
//
//	{dataDir}/plans/plan-{threadID}/
//	{dataDir}/sandboxes/sbx-{threadID}/
//
// Rejects any threadID that isn't the server-generated
// `thread-{unixnano}` format. Retains the HasPrefix
// belt-and-suspenders check in case the format changes in the
// future and someone forgets to re-tighten here.
func PlanDirForThread(dataDir, threadID string) (string, error) {
	if err := ValidateThreadID(threadID); err != nil {
		return "", err
	}
	root, err := filepath.Abs(filepath.Join(dataDir, "plans"))
	if err != nil {
		return "", fmt.Errorf("resolve plans root: %w", err)
	}
	dir := filepath.Clean(filepath.Join(root, "plan-"+threadID))
	if dir != root && !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid threadID: path escapes plans directory")
	}
	return dir, nil
}
