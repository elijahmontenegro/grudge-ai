package anthropic

import (
	"strings"
	"testing"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
)

func ptrInt32(v int32) *int32 { return &v }

// TestCodec_RequestRoundTrip — Anthropic's Messages format hoists
// the system prompt into a top-level field. EncodeRequest +
// DecodeRequest must restore the system as a ROLE_SYSTEM message at
// position 0 (canonical proto convention), and preserve the user
// turn that follows.
func TestCodec_RequestRoundTrip(t *testing.T) {
	c := Codec{}

	original := &pb.CompletionRequest{
		Model: "claude-3-5-sonnet-20241022",
		Messages: []*pb.LLMMessage{
			{
				Role: pb.Role_ROLE_SYSTEM,
				Content: []*pb.ContentBlock{
					{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "you are helpful"}}},
				},
			},
			{
				Role: pb.Role_ROLE_USER,
				Content: []*pb.ContentBlock{
					{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "hello"}}},
				},
			},
		},
		MaxTokens: ptrInt32(1024),
		Stream:    true,
	}

	body, err := c.EncodeRequest(original)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	decoded, err := c.DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	if decoded.Model != original.Model {
		t.Errorf("Model: got %q want %q", decoded.Model, original.Model)
	}
	if decoded.GetMaxTokens() != original.GetMaxTokens() {
		t.Errorf("MaxTokens: got %d want %d", decoded.GetMaxTokens(), original.GetMaxTokens())
	}
	if decoded.Stream != original.Stream {
		t.Errorf("Stream: got %v want %v", decoded.Stream, original.Stream)
	}
	if len(decoded.Messages) != len(original.Messages) {
		t.Fatalf("Messages length: got %d want %d", len(decoded.Messages), len(original.Messages))
	}
	for i, m := range decoded.Messages {
		o := original.Messages[i]
		if m.Role != o.Role {
			t.Errorf("Messages[%d].Role: got %v want %v", i, m.Role, o.Role)
		}
		if textOf(m.Content) != textOf(o.Content) {
			t.Errorf("Messages[%d].Content: got %q want %q", i, textOf(m.Content), textOf(o.Content))
		}
	}
}

// TestCodec_TextChunkRoundTrip — content_block_delta with
// type=text_delta survives encode/decode.
func TestCodec_TextChunkRoundTrip(t *testing.T) {
	c := Codec{}
	chunk := &pb.StreamChunk{
		Delta: &pb.StreamChunk_Text{Text: &pb.TextContent{Text: "hello"}},
	}
	wire, err := c.EncodeChunk(chunk)
	if err != nil {
		t.Fatalf("EncodeChunk: %v", err)
	}
	line := strings.TrimRight(string(wire), "\n")
	decoded, err := c.DecodeChunk([]byte(line))
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}
	if decoded == nil {
		t.Fatal("nil decoded chunk")
	}
	if got := decoded.GetText().GetText(); got != "hello" {
		t.Errorf("text: got %q want %q", got, "hello")
	}
}

// TestCodec_ThinkingChunkRoundTrip — Anthropic's thinking_delta
// type carries reasoning tokens; the codec maps them to the
// dedicated proto Thinking variant.
func TestCodec_ThinkingChunkRoundTrip(t *testing.T) {
	c := Codec{}
	chunk := &pb.StreamChunk{
		Delta: &pb.StreamChunk_Thinking{Thinking: &pb.ThinkingContent{Text: "let me think"}},
	}
	wire, err := c.EncodeChunk(chunk)
	if err != nil {
		t.Fatalf("EncodeChunk: %v", err)
	}
	line := strings.TrimRight(string(wire), "\n")
	decoded, err := c.DecodeChunk([]byte(line))
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}
	if decoded == nil {
		t.Fatal("nil decoded chunk")
	}
	if got := decoded.GetThinking().GetText(); got != "let me think" {
		t.Errorf("thinking: got %q want %q", got, "let me think")
	}
}

// TestCodec_DecodeChunk_SkipsNonData — keep-alives and event:
// lines (which Anthropic uses to name the event before the data:
// payload) must produce (nil, nil).
func TestCodec_DecodeChunk_SkipsNonData(t *testing.T) {
	c := Codec{}
	for _, line := range [][]byte{
		[]byte(""),
		[]byte("\n"),
		[]byte(": ping"),
		[]byte("event: message_start"),
	} {
		got, err := c.DecodeChunk(line)
		if err != nil {
			t.Errorf("DecodeChunk(%q) returned err: %v", line, err)
		}
		if got != nil {
			t.Errorf("DecodeChunk(%q) returned non-nil: %+v", line, got)
		}
	}
}

func textOf(blocks []*pb.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if t := b.GetText(); t != nil {
			sb.WriteString(t.GetText())
		}
	}
	return sb.String()
}
