package rrc

import (
	"strings"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
)

// TextFromBlocks concatenates content for display and approximate
// payload accounting. Selection indexing uses
// SerializeMessageForScoring so roles and block structure are retained.
//
// Includes: text blocks, thinking blocks, tool-call name+args
// ("Name: args"), tool-result content, attachment metadata
// ("[attached: filename at path]") plus any inlined attachment text.
//
// BlocksFromText is the inverse of TextFromBlocks for the simple case
// of a single plain-text turn. Wraps a string as one TextContent block.
// Used at boundaries that receive a bare text payload (GraphQL
// mutations, ADK function responses) and need to hand it to storage
// or the engine as proto-shaped content. Doesn't try to reconstruct
// thinking/toolcall/toolresult/attachment blocks — those originate
// from typed sources, not plain text.
func BlocksFromText(text string) []*pb.ContentBlock {
	return []*pb.ContentBlock{
		{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: text}}},
	}
}

func TextFromBlocks(blocks []*pb.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		switch {
		case b.GetText() != nil:
			sb.WriteString(b.GetText().Text)
			sb.WriteByte('\n')
		case b.GetThinking() != nil:
			sb.WriteString(b.GetThinking().Text)
			sb.WriteByte('\n')
		case b.GetToolCall() != nil:
			tc := b.GetToolCall()
			sb.WriteString(tc.Name)
			sb.WriteString(": ")
			sb.WriteString(tc.Arguments)
			sb.WriteByte('\n')
		case b.GetToolResult() != nil:
			sb.WriteString(b.GetToolResult().Content)
			sb.WriteByte('\n')
		case b.GetAttachment() != nil:
			a := b.GetAttachment()
			sb.WriteString("[attached: ")
			sb.WriteString(a.Filename)
			sb.WriteString(" at ")
			sb.WriteString(a.Path)
			sb.WriteString("]\n")
			if a.InlinedText != "" {
				sb.WriteString(a.InlinedText)
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}
