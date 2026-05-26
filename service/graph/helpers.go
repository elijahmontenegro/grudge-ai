package graph

import (
	"strings"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// (planDirForThread + threadIDPattern moved to service/runner/path.go.
// Both the runtime factory and the UpdatePlanSource resolver import it from
// there; graph no longer owns the pattern.)

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

