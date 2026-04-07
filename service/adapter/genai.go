package adapter

import (
	"strings"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// ProtoToText extracts all text content from proto content blocks.
func ProtoToText(blocks []*pb.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if t := b.GetText(); t != nil {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}

// TextToProto wraps a text string as a proto content block.
func TextToProto(text string) []*pb.ContentBlock {
	return []*pb.ContentBlock{
		{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: text}}},
	}
}

// MessageToLLM converts a corpus Message to an LLMMessage (wire format).
func MessageToLLM(msg *pb.Message) *pb.LLMMessage {
	return &pb.LLMMessage{
		Role:    msg.Role,
		Content: msg.Content,
	}
}

// MessagesToLLM converts a slice of Messages to LLMMessages.
func MessagesToLLM(msgs []*pb.Message) []*pb.LLMMessage {
	result := make([]*pb.LLMMessage, len(msgs))
	for i, m := range msgs {
		result[i] = MessageToLLM(m)
	}
	return result
}
