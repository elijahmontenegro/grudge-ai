package adk

import (
	"encoding/json"

	"github.com/elijahmontenegro/grudge/core/genaicodec"
	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
	"google.golang.org/genai"
)

// The genai <-> proto content mapping lives in core/genaicodec so the Vertex
// adapter and this ADK bridge share one implementation. These are thin
// re-exports preserving the adk-local names used across the package.

// GenaiContentToProto converts a single genai.Content to proto LLMMessage.
func GenaiContentToProto(c *genai.Content) *pb.LLMMessage { return genaicodec.ContentToProto(c) }

// ProtoToGenaiContent converts a proto LLMMessage to genai.Content.
func ProtoToGenaiContent(msg *pb.LLMMessage) *genai.Content { return genaicodec.ProtoToContent(msg) }

// ProtoResponseToGenai converts a proto CompletionResponse to genai Content.
func ProtoResponseToGenai(resp *pb.CompletionResponse) *genai.Content {
	return genaicodec.ProtoToContent(resp.Message)
}

// ExtractThinkingFromGenai extracts thinking blocks from genai Content.
func ExtractThinkingFromGenai(c *genai.Content) []*pb.ThinkingContent {
	return genaicodec.ExtractThinking(c)
}

// MarshalFunctionArgs serializes function call args to JSON.
func MarshalFunctionArgs(args map[string]any) (string, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return "{}", err
	}
	return string(b), nil
}
