package httpc

import (
	"fmt"
	"net/http"

	"github.com/elijahmontenegro/grudge/core"
)

// StatusError is the typed error returned when an upstream provider
// responds with a non-2xx status. Adapters wrap their non-2xx
// branches with one of these so the retry classifier can identify
// transient codes (408, 429, 5xx) without grepping error strings.
//
// Provider names the adapter for diagnostic output. StatusCode is
// the HTTP code as returned. Body is whatever bytes the provider
// included with the error response (truncated by the adapter to a
// sane bound — 8 KiB is typical).
//
// Unwrap maps the status code to the canonical core sentinel:
// 401/403 → core.ErrAuth, 429 → core.ErrRateLimited, anything else
// → core.ErrProviderUnavailable. Call sites use
// errors.Is(err, core.ErrAuth) etc. to classify failures.
type StatusError struct {
	Provider   string
	StatusCode int
	Body       string
}

// Error implements the error interface.
func (e *StatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("%s returned %d: %s", e.Provider, e.StatusCode, e.Body)
	}
	return fmt.Sprintf("%s returned %d", e.Provider, e.StatusCode)
}

// Unwrap returns the canonical core sentinel for the status code.
func (e *StatusError) Unwrap() error {
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return core.ErrAuth
	case http.StatusTooManyRequests:
		return core.ErrRateLimited
	default:
		return core.ErrProviderUnavailable
	}
}

// Status implements the duck-typed status-carrier contract used by
// core/httpc/retry. Returning the status code (not a Retryable
// boolean) keeps the retry policy in one place — retry owns the
// "what's transient" decision; httpc owns the typed error shape.
// Any adapter that wraps its own status-bearing error and exposes a
// `Status() int` method gets the same classification for free.
func (e *StatusError) Status() int { return e.StatusCode }
