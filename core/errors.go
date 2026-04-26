package core

import (
	"errors"
	"strings"
)

var (
	ErrUnsupported         = errors.New("capability not supported by provider")
	ErrAuth                = errors.New("authentication failed")
	ErrProviderUnavailable = errors.New("provider endpoint unreachable")
	ErrRateLimited         = errors.New("provider rate limit exceeded")

	// ErrContextTooLong indicates the request payload exceeded the
	// model's context-window ceiling. Distinguishing this from other
	// failures lets the assembly layer shed lowest-score Selected
	// entries and retry with a smaller payload, rather than surfacing
	// the error terminally. Detected via IsContextOverflow.
	ErrContextTooLong = errors.New("request exceeds model context window")

	// ErrModelNotFound indicates the requested model id is unknown to
	// the provider. Permanent — retrying won't fix a typo or a model
	// that's been deprecated.
	ErrModelNotFound = errors.New("model not found")
)

// IsContextOverflow reports whether the error's chain or message
// indicates a context-window-exceeded failure. The check is text-
// based because providers don't expose a single canonical code:
// OpenAI uses `context_length_exceeded`; Anthropic uses "prompt is
// too long"; Ollama upstream wraps minimax's "context window
// exceeds limit". Centralizing the pattern set here keeps every
// caller's overflow detection in one place — adapters that learn
// new wording add a single string here, not at every call site.
//
// errors.Is(err, ErrContextTooLong) returns true for any error in
// a chain wrapping ErrContextTooLong; this function additionally
// inspects the error's message for the known patterns, so adapters
// that haven't yet been updated to wrap with ErrContextTooLong
// still classify correctly.
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrContextTooLong) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, p := range contextOverflowPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// contextOverflowPatterns are observed wordings from each provider
// when the request exceeds the model's context window. New
// providers append here as their wording surfaces.
var contextOverflowPatterns = []string{
	"context window exceeds limit", // ollama (minimax upstream)
	"context length exceeded",      // OpenAI-family
	"maximum context length",       // OpenAI-family alt wording
	"context_length_exceeded",      // OpenAI error code
	"prompt is too long",           // Anthropic
	"input is too long",            // Anthropic alt
	"requested tokens",             // generic "exceeds" wording
	"exceeds the maximum",          // generic
	"too many tokens",              // generic
	"context window",               // last-resort catch
}
