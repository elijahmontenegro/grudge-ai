package openai

import (
	"strings"
	"testing"

	pb "github.com/emontenegr/spidey/proto/gen/go/spidey/v1"
)

func ptrInt32(v int32) *int32       { return &v }
func ptrFloat32(v float32) *float32 { return &v }

// TestCodec_RequestRoundTrip — EncodeRequest(DecodeRequest(body)) is
// equivalent to the original body for the fields the codec models.
// The proxy reads inbound, canonicalizes, runs RRC, re-emits — every
// hop must be lossless for what's in the schema, otherwise a
// downstream adapter sees stripped or mangled data.
func TestCodec_RequestRoundTrip(t *testing.T) {
	c := Codec{}

	original := &pb.CompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []*pb.LLMMessage{
			{
				Role: pb.Role_ROLE_USER,
				Content: []*pb.ContentBlock{
					{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "hello"}}},
				},
			},
			{
				Role: pb.Role_ROLE_ASSISTANT,
				Content: []*pb.ContentBlock{
					{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "hi"}}},
				},
			},
		},
		MaxTokens:   ptrInt32(100),
		Temperature: ptrFloat32(0.7),
		TopP:        ptrFloat32(0.9),
		Stop:        []string{"\n\n"},
		Stream:      true,
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
	if decoded.GetTemperature() != original.GetTemperature() {
		t.Errorf("Temperature: got %f want %f", decoded.GetTemperature(), original.GetTemperature())
	}
	if decoded.GetTopP() != original.GetTopP() {
		t.Errorf("TopP: got %f want %f", decoded.GetTopP(), original.GetTopP())
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
		gotText := textOf(m.Content)
		wantText := textOf(o.Content)
		if gotText != wantText {
			t.Errorf("Messages[%d].Content text: got %q want %q", i, gotText, wantText)
		}
	}
}

// TestCodec_TextChunkRoundTrip — a text-delta chunk survives an
// encode/decode cycle.
func TestCodec_TextChunkRoundTrip(t *testing.T) {
	c := Codec{}
	chunk := &pb.StreamChunk{
		ChoiceIndex: 0,
		Delta:       &pb.StreamChunk_Text{Text: &pb.TextContent{Text: "hello"}},
	}

	wire, err := c.EncodeChunk(chunk)
	if err != nil {
		t.Fatalf("EncodeChunk: %v", err)
	}
	// SSE shape: "data: {...}\n\n". DecodeChunk takes a single line so
	// strip the trailing blank line and pass just the data: line.
	line := strings.TrimRight(string(wire), "\n")
	decoded, err := c.DecodeChunk([]byte(line))
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}
	if decoded == nil {
		t.Fatal("DecodeChunk returned nil for text chunk")
	}
	if got := decoded.GetText().GetText(); got != "hello" {
		t.Errorf("text: got %q want %q", got, "hello")
	}
}

// TestCodec_ToolCallChunkRoundTrip — a tool-call chunk survives an
// encode/decode cycle including id, name, and arguments JSON.
func TestCodec_ToolCallChunkRoundTrip(t *testing.T) {
	c := Codec{}
	chunk := &pb.StreamChunk{
		ChoiceIndex: 0,
		Delta: &pb.StreamChunk_ToolCall{ToolCall: &pb.ToolCallContent{
			Id:        "call_1",
			Name:      "Bash",
			Arguments: `{"command":"ls -la"}`,
		}},
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
		t.Fatal("DecodeChunk returned nil for tool-call chunk")
	}
	tc := decoded.GetToolCall()
	if tc == nil {
		t.Fatal("decoded chunk missing tool_call")
	}
	if tc.Id != "call_1" {
		t.Errorf("Id: got %q want %q", tc.Id, "call_1")
	}
	if tc.Name != "Bash" {
		t.Errorf("Name: got %q want %q", tc.Name, "Bash")
	}
	if tc.Arguments != `{"command":"ls -la"}` {
		t.Errorf("Arguments: got %q want %q", tc.Arguments, `{"command":"ls -la"}`)
	}
}

// TestCodec_DoneChunk — the [DONE] sentinel encodes as a known
// wire value and decodes as a Done=true chunk.
func TestCodec_DoneChunk(t *testing.T) {
	c := Codec{}
	wire, err := c.EncodeChunk(&pb.StreamChunk{Done: true})
	if err != nil {
		t.Fatalf("EncodeChunk(Done): %v", err)
	}
	if !strings.Contains(string(wire), "[DONE]") {
		t.Errorf("Done chunk wire missing [DONE] sentinel: %q", string(wire))
	}
	line := strings.TrimRight(string(wire), "\n")
	decoded, err := c.DecodeChunk([]byte(line))
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}
	if decoded == nil || !decoded.Done {
		t.Errorf("expected Done=true, got %v", decoded)
	}
}

// TestCodec_DecodeChunk_SkipsNonData — keep-alive comments and blank
// lines must produce (nil, nil) rather than errors.
func TestCodec_DecodeChunk_SkipsNonData(t *testing.T) {
	c := Codec{}
	for _, line := range [][]byte{
		[]byte(""),
		[]byte("\n"),
		[]byte(": keep-alive"),
		[]byte("event: ping"),
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

// textOf flattens a content slice to its text concatenation, ignoring
// non-text blocks. Mirrors what the codec's helpers do internally.
func textOf(blocks []*pb.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if t := b.GetText(); t != nil {
			sb.WriteString(t.GetText())
		}
	}
	return sb.String()
}

