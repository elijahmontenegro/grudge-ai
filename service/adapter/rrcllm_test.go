package adapter

import (
	"testing"
)

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
			if !isContextOverflow(tc.err) {
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
		if isContextOverflow(e) {
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
