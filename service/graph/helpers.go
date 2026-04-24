package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// threadIDPattern enforces the server-generated thread ID format
// (CreateThread emits `thread-{unixnano}`). Refusing anything else at the
// plan-dir boundary prevents two classes of abuse from client-supplied IDs:
//  1. Hard traversal (../etc/foo) that escapes plansRoot.
//  2. Soft traversal (../../etc/foo) whose filepath.Clean result stays
//     inside plansRoot but outside the plan-{id} convention — the agent's
//     planGuard only checks HasPrefix(PlanDir, root), so a crafted threadID
//     could pollute arbitrary subdirs under plans/.
var threadIDPattern = regexp.MustCompile(`^thread-\d+$`)

// planDirForThread returns the absolute plan directory for a thread. Rejects
// any threadID that isn't the server-generated `thread-{unixnano}` format.
// Retains the HasPrefix belt-and-suspenders check in case the format changes
// in the future and someone forgets to re-tighten here.
func planDirForThread(dataDir, threadID string) (string, error) {
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

// protoRoleToDisplay maps proto roles to frontend-friendly names.
// Decouples the frontend from proto enum string representations.
func protoRoleToDisplay(r pb.Role) string {
	switch r {
	case pb.Role_ROLE_USER:
		return "user"
	case pb.Role_ROLE_ASSISTANT:
		return "assistant"
	case pb.Role_ROLE_SYSTEM:
		return "system"
	default:
		return "unknown"
	}
}

// protoContentToDisplay extracts text-only content from proto blocks.
// Tool calls and results are returned via separate structured resolvers.
func protoContentToDisplay(blocks []*pb.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if t := b.GetText(); t != nil {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// protoToolCalls extracts tool call blocks from proto content.
func protoToolCalls(blocks []*pb.ContentBlock) []*pb.ToolCallContent {
	var calls []*pb.ToolCallContent
	for _, b := range blocks {
		if tc := b.GetToolCall(); tc != nil {
			calls = append(calls, tc)
		}
	}
	return calls
}

// protoToolResults extracts tool result blocks from proto content.
func protoToolResults(blocks []*pb.ContentBlock) []*pb.ToolResultContent {
	var results []*pb.ToolResultContent
	for _, b := range blocks {
		if tr := b.GetToolResult(); tr != nil {
			results = append(results, tr)
		}
	}
	return results
}

// protoThinkingContent extracts thinking text from content blocks.
func protoThinkingContent(blocks []*pb.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if t := b.GetThinking(); t != nil {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// attachmentInputsToBlocks converts GraphQL AttachmentInput values into
// proto AttachmentContent blocks. The client has already uploaded files
// via POST /api/attachments/{threadId} and echoed back the metadata —
// this is a pure shape transform, no re-validation.
func attachmentInputsToBlocks(inputs []*AttachmentInput) []*pb.AttachmentContent {
	out := make([]*pb.AttachmentContent, 0, len(inputs))
	for _, a := range inputs {
		if a == nil {
			continue
		}
		inlined := ""
		if a.InlinedText != nil {
			inlined = *a.InlinedText
		}
		out = append(out, &pb.AttachmentContent{
			Id:          a.ID,
			Filename:    a.Filename,
			MimeType:    a.MimeType,
			SizeBytes:   int64(a.SizeBytes),
			Path:        a.Path,
			InlinedText: inlined,
		})
	}
	return out
}

