package graph

import (
	"strings"

	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// protoRoleToDisplay maps proto roles to frontend-friendly names.
// Decouples the frontend from proto enum string representations.
func protoRoleToDisplay(r threadv1.Role) string {
	switch r {
	case threadv1.Role_ROLE_USER:
		return "user"
	case threadv1.Role_ROLE_ASSISTANT:
		return "assistant"
	case threadv1.Role_ROLE_SYSTEM:
		return "system"
	default:
		return "unknown"
	}
}

// protoContentToDisplay extracts text-only content from proto blocks.
// Tool calls and results are returned via separate structured resolvers.
func protoContentToDisplay(blocks []*threadv1.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if t := b.GetText(); t != nil {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// protoToolCalls extracts tool call blocks from proto content.
func protoToolCalls(blocks []*threadv1.ContentBlock) []*threadv1.ToolCallContent {
	var calls []*threadv1.ToolCallContent
	for _, b := range blocks {
		if tc := b.GetToolCall(); tc != nil {
			calls = append(calls, tc)
		}
	}
	return calls
}

// protoToolResults extracts tool result blocks from proto content.
func protoToolResults(blocks []*threadv1.ContentBlock) []*threadv1.ToolResultContent {
	var results []*threadv1.ToolResultContent
	for _, b := range blocks {
		if tr := b.GetToolResult(); tr != nil {
			results = append(results, tr)
		}
	}
	return results
}

// protoThinkingContent extracts thinking text from content blocks.
func protoThinkingContent(blocks []*threadv1.ContentBlock) string {
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
func attachmentInputsToBlocks(inputs []*AttachmentInput) []*threadv1.AttachmentContent {
	out := make([]*threadv1.AttachmentContent, 0, len(inputs))
	for _, a := range inputs {
		if a == nil {
			continue
		}
		inlined := ""
		if a.InlinedText != nil {
			inlined = *a.InlinedText
		}
		out = append(out, &threadv1.AttachmentContent{
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
