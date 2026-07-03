package vertex

import (
	"testing"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
	"google.golang.org/genai"
)

// TestEncode_HoistsSystemInstructionAndParams confirms the request codec
// pulls the SYSTEM message out of the turn list into SystemInstruction (genai
// has no system role) and threads generation params.
func TestEncode_HoistsSystemInstructionAndParams(t *testing.T) {
	c := &completer{model: "gemini-2.5-pro"}
	maxTok := int32(256)
	temp := float32(0.3)
	req := &pb.CompletionRequest{
		Messages: []*pb.LLMMessage{
			{Role: pb.Role_ROLE_SYSTEM, Content: []*pb.ContentBlock{
				{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "be terse"}}},
			}},
			{Role: pb.Role_ROLE_USER, Content: []*pb.ContentBlock{
				{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "hi"}}},
			}},
		},
		MaxTokens:   &maxTok,
		Temperature: &temp,
	}

	contents, cfg := c.encode(req)

	if cfg.SystemInstruction == nil {
		t.Fatal("system message not hoisted into SystemInstruction")
	}
	// Only the user turn remains in contents.
	if len(contents) != 1 || contents[0].Role != "user" {
		t.Fatalf("system leaked into contents or user missing: %+v", contents)
	}
	if cfg.MaxOutputTokens != 256 {
		t.Fatalf("max tokens not threaded: %d", cfg.MaxOutputTokens)
	}
	if cfg.Temperature == nil || *cfg.Temperature != 0.3 {
		t.Fatalf("temperature not threaded: %v", cfg.Temperature)
	}
}

// TestEncode_ToolDeclarations confirms tool declarations become genai
// FunctionDeclarations with the JSON schema passed through as a parsed object.
func TestEncode_ToolDeclarations(t *testing.T) {
	c := &completer{model: "gemini-2.5-pro"}
	req := &pb.CompletionRequest{
		Messages: []*pb.LLMMessage{
			{Role: pb.Role_ROLE_USER, Content: []*pb.ContentBlock{
				{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: "hi"}}},
			}},
		},
		Tools: []*pb.ToolDeclaration{
			{Name: "read", Description: "read a file", ParametersJson: `{"type":"object","properties":{"path":{"type":"string"}}}`},
		},
	}
	_, cfg := c.encode(req)
	if len(cfg.Tools) != 1 || len(cfg.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tool not encoded: %+v", cfg.Tools)
	}
	fd := cfg.Tools[0].FunctionDeclarations[0]
	if fd.Name != "read" || fd.Description != "read a file" {
		t.Fatalf("tool name/desc wrong: %+v", fd)
	}
	if fd.ParametersJsonSchema == nil {
		t.Fatal("tool parameters schema not passed through")
	}
}

// TestPartToChunk_SynthesizesCallID confirms a Gemini function call with an
// empty ID gets a stable synthesized id (grudge correlates tool results by
// id; an empty id would break multi-turn tool loops).
func TestPartToChunk_SynthesizesCallID(t *testing.T) {
	chunk := partToChunk(&genai.Part{FunctionCall: &genai.FunctionCall{Name: "read", Args: map[string]any{"path": "x"}}})
	tc := chunk.GetToolCall()
	if tc == nil || tc.Id == "" {
		t.Fatalf("empty function-call id not synthesized: %+v", tc)
	}
	if tc.Id != "vertex-call-read" {
		t.Fatalf("synthesized id not stable/derived from name: %q", tc.Id)
	}
	// An explicit id is preserved.
	chunk2 := partToChunk(&genai.Part{FunctionCall: &genai.FunctionCall{ID: "abc", Name: "read"}})
	if chunk2.GetToolCall().Id != "abc" {
		t.Fatal("explicit call id not preserved")
	}
}

func TestNormalizeFinish(t *testing.T) {
	if normalizeFinish(genai.FinishReasonStop) != "stop" {
		t.Fatal("STOP should normalize to stop")
	}
	if normalizeFinish(genai.FinishReasonMaxTokens) != "length" {
		t.Fatal("MAX_TOKENS should normalize to length")
	}
	if normalizeFinish("") != "" {
		t.Fatal("empty finish should stay empty")
	}
}
