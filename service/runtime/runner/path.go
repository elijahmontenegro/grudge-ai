package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// threadIDPattern enforces the server-generated thread ID format
// (CreateThread emits `thread-{unixnano}`). Refusing anything else
// at the plan-dir boundary prevents two classes of abuse from
// client-supplied IDs:
//
//  1. Hard traversal (../etc/foo) that escapes plansRoot.
//  2. Soft traversal (../../etc/foo) whose filepath.Clean result
//     stays inside plansRoot but outside the plan-{id} convention —
//     the agent's planGuard only checks HasPrefix(PlanDir, root),
//     so a crafted threadID could pollute arbitrary subdirs under
//     plans/.
//
// Mirrored in service/api/attachments.go and service/sandbox/workspace.go;
// any change here needs to ripple to those.
var threadIDPattern = regexp.MustCompile(`^thread-\d+$`)

// PlanDirForThread returns the absolute plan directory for a
// thread. Rejects any threadID that isn't the server-generated
// `thread-{unixnano}` format. Retains the HasPrefix
// belt-and-suspenders check in case the format changes in the
// future and someone forgets to re-tighten here.
func PlanDirForThread(dataDir, threadID string) (string, error) {
	if threadID == "" {
		return "", fmt.Errorf("empty threadID")
	}
	if !threadIDPattern.MatchString(threadID) {
		return "", fmt.Errorf("invalid threadID format: %q", threadID)
	}
	root, err := filepath.Abs(filepath.Join(dataDir, "plans"))
	if err != nil {
		return "", fmt.Errorf("resolve plans root: %w", err)
	}
	dir := filepath.Clean(filepath.Join(root, fmt.Sprintf("plan-%s", threadID)))
	if dir != root && !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid threadID: path escapes plans directory")
	}
	return dir, nil
}
