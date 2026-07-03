package adkbridge

import (
	"testing"

	"github.com/elijahmontenegro/grudge/core"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	"github.com/elijahmontenegro/grudge/rrc/chunk"
)

// testEstimator gives every engine config in this file a deterministic
// token estimator (the estimator lives on chunk.Config, not a global).
type testEstimator struct{}

func (testEstimator) Estimate(s string) int { return len(s)/4 + 1 }

func testChunkCfg() chunk.Config {
	cfg := chunk.DefaultConfig()
	cfg.Estimator = testEstimator{}
	return cfg
}

// --- isContextOverflow pattern coverage ---
//
// This function gates the reactive shed loop. A false negative
// (real overflow not recognized) means the round surfaces as a
// terminal error instead of self-healing by shedding low-score
// Selected entries. A false positive (unrelated error misclassified
// as overflow) means we shed on every provider hiccup until we have
// nothing left to shed. Both paths are costly, so the pattern set
// deserves direct tests — one per known pattern plus a negative.

func TestIsContextOverflow_KnownPatterns(t *testing.T) {
	// Every pattern listed in rrcllm.go must match. Track regressions
	// by naming the pattern in the subtest.
	cases := []struct {
		name string
		err  error
	}{
		{"ollama: context window exceeds limit",
			errFromString("ollama: context window exceeds limit")},
		{"OpenAI: context length exceeded",
			errFromString("400 bad request: context length exceeded")},
		{"OpenAI: maximum context length",
			errFromString("maximum context length is 128000 tokens")},
		{"OpenAI code",
			errFromString("context_length_exceeded")},
		{"Anthropic: prompt is too long",
			errFromString("prompt is too long: ...")},
		{"Anthropic alt",
			errFromString("input is too long")},
		{"Generic requested-tokens wording",
			errFromString("requested tokens exceed the limit")},
		{"Generic exceeds-the-maximum",
			errFromString("token count exceeds the maximum")},
		{"Generic too-many-tokens",
			errFromString("error: too many tokens in the request")},
		{"Last-resort substring",
			errFromString("error about context window")},
		{"Case insensitive",
			errFromString("CONTEXT LENGTH EXCEEDED")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !core.IsContextOverflow(tc.err) {
				t.Fatalf("should match known pattern: %q", tc.err.Error())
			}
		})
	}
}

func TestIsContextOverflow_UnrelatedErrors(t *testing.T) {
	// Anything that doesn't match a known pattern must return false —
	// otherwise the reactive shed loop fires on routine errors.
	cases := []error{
		errFromString("connection reset by peer"),
		errFromString("503 service unavailable"),
		errFromString("rate limit exceeded"), // deliberately ambiguous — contains "exceed" but not a known pattern substring
		errFromString("invalid api key"),
		nil,
	}
	for _, e := range cases {
		if core.IsContextOverflow(e) {
			if e == nil {
				t.Fatalf("nil should not match overflow")
			}
			t.Fatalf("should NOT match: %q", e.Error())
		}
	}
}

// errFromString is a small helper so tests read naturally — inline
// `fmt.Errorf` with string arg reads the same, but we use a typed
// value for clarity.
type stringErr string

func (s stringErr) Error() string { return string(s) }

func errFromString(s string) error { return stringErr(s) }

// --- estimateToolSchemaTokens (Phase A.2) ---
//
// The budget estimate must include tool-schema overhead — 22 function
// declarations at ~300 bytes each is a non-trivial slice of the
// per-request payload. These tests lock in the serialization shape
// (matches the Ollama adapter's emission) and guard against drift.

// TestEstimateToolSchemaTokens_Empty returns zero on an empty slice
// without any tiktoken overhead — a round with no tools should carry
// no tool-side budget line item at all.
func TestEstimateToolSchemaTokens_Empty(t *testing.T) {
	if got := estimateToolSchemaTokens(nil, testChunkCfg()); got != 0 {
		t.Errorf("empty tools: got %d, want 0", got)
	}
	if got := estimateToolSchemaTokens([]*llmv1.ToolDeclaration{}, testChunkCfg()); got != 0 {
		t.Errorf("empty slice tools: got %d, want 0", got)
	}
}

// TestEstimateToolSchemaTokens_MatchesEncode verifies the estimate is
// the tiktoken count of the serialized JSON for each tool. Precise
// equality against a hand-computed token count is provider-tokenizer-
// specific; instead the test asserts the estimate is strictly
// positive and larger for a larger description — confirming each
// tool contributes and the estimate tracks serialized size.
func TestEstimateToolSchemaTokens_MatchesEncode(t *testing.T) {
	short := []*llmv1.ToolDeclaration{{
		Name:           "Read",
		Description:    "Read a file",
		ParametersJson: `{"type":"object","properties":{"path":{"type":"string"}}}`,
	}}
	long := []*llmv1.ToolDeclaration{{
		Name: "Read",
		Description: "Read a file from the filesystem. Accepts an absolute " +
			"path and returns its contents. Handles UTF-8 and binary files. " +
			"Errors if the path is outside the workspace or does not exist.",
		ParametersJson: `{"type":"object","properties":{"path":{"type":"string"}}}`,
	}}

	shortTokens := estimateToolSchemaTokens(short, testChunkCfg())
	longTokens := estimateToolSchemaTokens(long, testChunkCfg())

	if shortTokens <= 0 {
		t.Errorf("short estimate must be positive; got %d", shortTokens)
	}
	if longTokens <= shortTokens {
		t.Errorf("longer description should yield higher estimate; got short=%d long=%d",
			shortTokens, longTokens)
	}
}

// TestEstimateToolSchemaTokens_AccumulatesAcrossTools verifies each
// tool contributes to the total — i.e. the loop doesn't short-circuit
// or accidentally overwrite the running sum.
func TestEstimateToolSchemaTokens_AccumulatesAcrossTools(t *testing.T) {
	one := []*llmv1.ToolDeclaration{{
		Name:           "A",
		Description:    "first",
		ParametersJson: `{"type":"object"}`,
	}}
	two := []*llmv1.ToolDeclaration{
		one[0],
		{Name: "B", Description: "second", ParametersJson: `{"type":"object"}`},
	}
	t1 := estimateToolSchemaTokens(one, testChunkCfg())
	t2 := estimateToolSchemaTokens(two, testChunkCfg())
	if t2 <= t1 {
		t.Errorf("two-tool estimate must exceed one-tool; got one=%d two=%d", t1, t2)
	}
}

// hasTextBlock tests live in rrc/assembly_test.go — the helper now
// lives in rrc because Local Context construction is part of Engine.Assemble.
