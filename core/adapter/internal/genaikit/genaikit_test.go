package genaikit

import (
	"strings"
	"testing"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"google.golang.org/genai"
)

// TestEncode_HoistsSystemInstructionAndParams confirms the request codec
// pulls the SYSTEM message out of the turn list into SystemInstruction (genai
// has no system role) and threads generation params.
func TestEncode_HoistsSystemInstructionAndParams(t *testing.T) {
	c := &Completer{Model: "gemini-2.5-pro"}
	maxTok := int32(256)
	temp := float32(0.3)
	req := &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{
			{Role: threadv1.Role_ROLE_SYSTEM, Content: []*threadv1.ContentBlock{
				{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "be terse"}}},
			}},
			{Role: threadv1.Role_ROLE_USER, Content: []*threadv1.ContentBlock{
				{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "hi"}}},
			}},
		},
		MaxTokens:   &maxTok,
		Temperature: &temp,
	}

	contents, cfg, err := c.encode(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

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
	c := &Completer{Model: "gemini-2.5-pro"}
	req := &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{
			{Role: threadv1.Role_ROLE_USER, Content: []*threadv1.ContentBlock{
				{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "hi"}}},
			}},
		},
		Tools: []*llmv1.ToolDeclaration{
			{Name: "read", Description: "read a file", ParametersJson: `{"type":"object","properties":{"path":{"type":"string"}}}`},
		},
	}
	_, cfg, err := c.encode(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
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

// All four ToolChoice modes map to genai's ToolConfig; unset/UNSPECIFIED
// omits it. NAMED maps to ANY restricted to one AllowedFunctionNames
// entry (genai has no single-tool-forced mode).
func TestEncode_ToolChoiceModes(t *testing.T) {
	c := &Completer{Model: "gemini-2.5-pro"}
	base := &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{{Role: threadv1.Role_ROLE_USER, Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "hi"}}},
		}}},
	}
	_, cfg, err := c.encode(base)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if cfg.ToolConfig != nil {
		t.Fatalf("unset ToolChoice must omit ToolConfig, got: %+v", cfg.ToolConfig)
	}

	cases := []struct {
		mode     llmv1.ToolChoiceMode
		name     string
		wantMode genai.FunctionCallingConfigMode
		wantFns  []string
	}{
		{llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_AUTO, "", genai.FunctionCallingConfigModeAuto, nil},
		{llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NONE, "", genai.FunctionCallingConfigModeNone, nil},
		{llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_REQUIRED, "", genai.FunctionCallingConfigModeAny, nil},
		{llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NAMED, "Bash", genai.FunctionCallingConfigModeAny, []string{"Bash"}},
	}
	for _, tc := range cases {
		req := &llmv1.CompletionRequest{
			Messages:   base.Messages,
			ToolChoice: &llmv1.ToolChoice{Mode: tc.mode, NamedTool: tc.name},
		}
		_, cfg, err := c.encode(req)
		if err != nil {
			t.Fatalf("mode %v: encode: %v", tc.mode, err)
		}
		if cfg.ToolConfig == nil || cfg.ToolConfig.FunctionCallingConfig == nil {
			t.Fatalf("mode %v: ToolConfig not set", tc.mode)
		}
		got := cfg.ToolConfig.FunctionCallingConfig
		if got.Mode != tc.wantMode {
			t.Fatalf("mode %v: FunctionCallingConfig.Mode = %v, want %v", tc.mode, got.Mode, tc.wantMode)
		}
		if len(got.AllowedFunctionNames) != len(tc.wantFns) {
			t.Fatalf("mode %v: AllowedFunctionNames = %v, want %v", tc.mode, got.AllowedFunctionNames, tc.wantFns)
		}
	}

	// NAMED with an empty tool name can't be encoded — refuse rather
	// than silently falling back to AUTO.
	_, _, err = c.encode(&llmv1.CompletionRequest{
		Messages:   base.Messages,
		ToolChoice: &llmv1.ToolChoice{Mode: llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NAMED},
	})
	if err == nil {
		t.Fatal("NAMED with empty named_tool should error, got nil")
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
	if tc.Id != "genai-call-read" {
		t.Fatalf("synthesized id not stable/derived from name: %q", tc.Id)
	}
	// An explicit id is preserved.
	chunk2 := partToChunk(&genai.Part{FunctionCall: &genai.FunctionCall{ID: "abc", Name: "read"}})
	if chunk2.GetToolCall().Id != "abc" {
		t.Fatal("explicit call id not preserved")
	}
}

// partToChunk carries ThoughtSignature through on both a thinking
// part and a function-call part — it's Part-level, so a signed part
// needs no separate terminator chunk (contrast Anthropic's SSE
// protocol via the bridge).
func TestPartToChunk_CarriesThoughtSignature(t *testing.T) {
	thinkChunk := partToChunk(&genai.Part{Text: "reasoning", Thought: true, ThoughtSignature: []byte("sig-a")})
	th := thinkChunk.GetThinking()
	if th == nil || th.Text != "reasoning" || string(th.Signature) != "sig-a" {
		t.Fatalf("thinking signature not carried: %+v", th)
	}

	sigOnly := partToChunk(&genai.Part{Thought: true, ThoughtSignature: []byte("sig-only")})
	th2 := sigOnly.GetThinking()
	if th2 == nil || th2.Text != "" || string(th2.Signature) != "sig-only" {
		t.Fatalf("signature-only thought part not carried: %+v", th2)
	}

	callChunk := partToChunk(&genai.Part{FunctionCall: &genai.FunctionCall{ID: "c1", Name: "read"}, ThoughtSignature: []byte("sig-b")})
	tc := callChunk.GetToolCall()
	if tc == nil || string(tc.Signature) != "sig-b" {
		t.Fatalf("function-call signature not carried: %+v", tc)
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

// CountText mirrors ProtoToContent: text, thinking (sent as Thought
// parts), tool calls, and tool results all reach the wire; attachments
// never do.
func TestCountText_MirrorsGenaiCodec(t *testing.T) {
	m := &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "answer"}}},
			{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "REASONING"}}},
			{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{Id: "c1", Name: "Bash", Arguments: `{"cmd":"ls"}`}}},
			{Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{ToolCallId: "c1", Content: "a.txt"}}},
			{Block: &threadv1.ContentBlock_Attachment{Attachment: &threadv1.AttachmentContent{
				Filename: "SECRET.pdf", Path: "/x", InlinedText: "INLINED",
			}}},
		},
	}
	got := CountText(m)
	for _, want := range []string{"answer", "REASONING", "Bash", "a.txt"} {
		if !strings.Contains(got, want) {
			t.Fatalf("CountText missing %q: %q", want, got)
		}
	}
	for _, banned := range []string{"SECRET.pdf", "INLINED"} {
		if strings.Contains(got, banned) {
			t.Fatalf("CountText includes attachment content %q: %q", banned, got)
		}
	}
}
