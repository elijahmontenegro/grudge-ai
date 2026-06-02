package rrc

import (
	"strings"

	pb "github.com/emontenegr/grudge/proto/gen/go/grudge/v1"
)

// TextFromBlocks concatenates every scorable block in a content list
// into a single newline-joined string. Single source of truth for
// "what counts as scorable text on a message" — the rrc engine,
// service/storage chunker, service/agent/adk budget estimator, and
// boot-time chunk backfill all share this. A divergent copy would
// silently invalidate the chunk vector cache (the embedder would key
// off a different string than the chunker stored).
//
// Includes: text blocks, thinking blocks, tool-call name+args
// ("Name: args"), tool-result content, attachment metadata
// ("[attached: filename at path]") plus any inlined attachment text.
//
// Tool blocks are included so autonomous rounds — which step through
// tool_call / tool_result as distinct messages — get scorable text
// on every node. Skipping them would leave a tool-only turn empty,
// the engine's empty-text guard would bail, no edges would form, and
// the next round's selection would start from a node with no incoming
// edges.
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
