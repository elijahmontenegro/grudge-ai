package httpc

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/elijahmontenegro/grudge/core"
)

// TestNewStatusError_CapturesBody is the defect fix: streaming error paths
// previously built StatusError without a body, so a streaming 429/5xx lost
// its diagnostic message. NewStatusError must capture (and truncate) it.
func TestNewStatusError_CapturesBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       io.NopCloser(strings.NewReader("  rate limit exceeded: retry in 30s  ")),
	}
	e := NewStatusError("openai", resp)

	if e.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", e.StatusCode)
	}
	if e.Body != "rate limit exceeded: retry in 30s" {
		t.Fatalf("body not captured/trimmed: %q", e.Body)
	}
	// Classifies as rate-limited via Unwrap (so retry treats it correctly).
	if !errors.Is(e, core.ErrRateLimited) {
		t.Fatal("429 should unwrap to ErrRateLimited")
	}
	// The body appears in Error() for logs.
	if !strings.Contains(e.Error(), "retry in 30s") {
		t.Fatalf("Error() dropped body: %q", e.Error())
	}
}

// TestNewStatusError_Truncates confirms an oversized body is bounded.
func TestNewStatusError_Truncates(t *testing.T) {
	huge := strings.Repeat("x", 100<<10) // 100 KiB
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(huge)),
	}
	e := NewStatusError("anthropic", resp)
	if len(e.Body) > statusBodyLimit {
		t.Fatalf("body not truncated: %d bytes", len(e.Body))
	}
}

// TestNewStatusError_NilBody must not panic.
func TestNewStatusError_NilBody(t *testing.T) {
	e := NewStatusError("googleai", &http.Response{StatusCode: 503})
	if e.Body != "" || e.StatusCode != 503 {
		t.Fatalf("unexpected: %+v", e)
	}
}
