package genaicodec

import (
	"testing"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"google.golang.org/genai"
)

// TestRoundTrip_TextThinkingToolCallToolResult confirms proto→genai→proto
// preserves each content block type and the fields adapters correlate on
// (tool-call id, tool-result id, thinking flag).
func TestRoundTrip_TextThinkingToolCallToolResult(t *testing.T) {
	orig := &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: "hello"}}},
			{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: "reasoning"}}},
			{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{
				Id: "call-1", Name: "read", Arguments: `{"path":"x"}`,
			}}},
			{Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{
				ToolCallId: "call-1", Content: `{"answers":{"a":"b"}}`,
			}}},
		},
	}

	got := ContentToProto(ProtoToContent(orig))

	if got.Role != threadv1.Role_ROLE_ASSISTANT {
		t.Fatalf("role not preserved: %v", got.Role)
	}
	if len(got.Content) != 4 {
		t.Fatalf("block count changed: %d", len(got.Content))
	}
	if got.Content[0].GetText().Text != "hello" {
		t.Fatalf("text block lost: %+v", got.Content[0])
	}
	if got.Content[1].GetThinking().Text != "reasoning" {
		t.Fatalf("thinking block lost: %+v", got.Content[1])
	}
	tc := got.Content[2].GetToolCall()
	if tc == nil || tc.Id != "call-1" || tc.Name != "read" {
		t.Fatalf("tool-call id/name not preserved: %+v", tc)
	}
	tr := got.Content[3].GetToolResult()
	if tr == nil || tr.ToolCallId != "call-1" {
		t.Fatalf("tool-result id not preserved: %+v", tr)
	}
	// The full tool-result JSON must survive (not collapsed to {result:""}).
	if tr.Content == "" || tr.Content == `{"result":""}` {
		t.Fatalf("tool-result content collapsed: %q", tr.Content)
	}
}

// TestSignatureRoundTrip confirms a thought signature and a
// function-call signature both survive proto->genai->proto
// byte-identical. ThoughtSignature is Part-level in the genai SDK
// (sibling of Text/Thought and of FunctionCall), so both must map
// through without being dropped or corrupted.
func TestSignatureRoundTrip(t *testing.T) {
	orig := &llmv1.LLMMessage{
		Role: threadv1.Role_ROLE_ASSISTANT,
		Content: []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{
				Text: "reasoning", Signature: []byte("thought-sig-bytes"),
			}}},
			{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{
				Id: "call-1", Name: "read", Arguments: `{"path":"x"}`, Signature: []byte("call-sig-bytes"),
			}}},
		},
	}

	genaiContent := ProtoToContent(orig)
	if string(genaiContent.Parts[0].ThoughtSignature) != "thought-sig-bytes" {
		t.Fatalf("thought signature not set on the genai Part: %+v", genaiContent.Parts[0])
	}
	if string(genaiContent.Parts[1].ThoughtSignature) != "call-sig-bytes" {
		t.Fatalf("function-call signature not set on the genai Part: %+v", genaiContent.Parts[1])
	}

	got := ContentToProto(genaiContent)
	if string(got.Content[0].GetThinking().Signature) != "thought-sig-bytes" {
		t.Fatalf("thought signature lost on the way back: %+v", got.Content[0])
	}
	if string(got.Content[1].GetToolCall().Signature) != "call-sig-bytes" {
		t.Fatalf("function-call signature lost on the way back: %+v", got.Content[1])
	}
}

// TestContentToProto_SignatureOnlyThoughtPartSurvives confirms a
// thought part with empty text but a signature is not silently
// dropped by the text != "" gate.
func TestContentToProto_SignatureOnlyThoughtPartSurvives(t *testing.T) {
	c := &genai.Content{Role: "model", Parts: []*genai.Part{
		{Thought: true, ThoughtSignature: []byte("sig-only")},
	}}
	msg := ContentToProto(c)
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(msg.Content))
	}
	th := msg.Content[0].GetThinking()
	if th == nil || th.Text != "" || string(th.Signature) != "sig-only" {
		t.Fatalf("signature-only thought part not preserved: %+v", th)
	}
}

// TestRoleMapping confirms the system-role carrier behavior (genai has no
// system role → carried as "user"; the SDK-side system instruction is
// separate).
func TestRoleMapping(t *testing.T) {
	if RoleToGenai(threadv1.Role_ROLE_SYSTEM) != "user" {
		t.Fatal("system should map to user carrier for genai")
	}
	if RoleToGenai(threadv1.Role_ROLE_ASSISTANT) != "model" {
		t.Fatal("assistant should map to model")
	}
	if RoleToProto("model") != threadv1.Role_ROLE_ASSISTANT {
		t.Fatal("model should map to assistant")
	}
}
