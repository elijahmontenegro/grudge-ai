// Package resilience provides a retry wrapper with bounded attempts,
// exponential backoff, and a progress callback the service can bridge
// into user-facing UI events. The intent is principled retry, not
// silent sleep-retry: every attempt reports an Event so the caller
// can render "retrying 2/10, next attempt in 8s (503 Service
// Unavailable)" to the user. Context cancellation aborts retries
// immediately.
package resilience

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"time"

	"github.com/emontenegr/spidey/core"
)

// Policy controls retry behavior.
type Policy struct {
	// MaxAttempts is the total attempts including the first try.
	// MaxAttempts <= 1 disables retry.
	MaxAttempts int
	// BaseDelay is the wait before the first retry.
	BaseDelay time.Duration
	// MaxDelay caps the wait (so exponential growth doesn't run away).
	MaxDelay time.Duration
	// Multiplier applies between attempts (e.g. 2.0 doubles).
	Multiplier float64
	// Jitter is the fraction of delay randomized (0..1). A jitter of
	// 0.2 means the actual wait is in [0.8, 1.2] × computed delay.
	Jitter float64
}

// DefaultPolicy: 10 attempts, 5s → 5min backoff, 2× multiplier, 20% jitter.
//
// Designed for multi-hour autonomous runs where real-world transient
// failures routinely last longer than a few minutes — cloud provider
// incidents, rate-limit storms, local container restarts (TEI, Docker
// sandbox), and transient DNS hiccups all fall in the 10-30-minute
// range. The prior 2s→60s caps gave up inside 6 minutes of sustained
// failure and were killing long runs over fixable problems.
//
// Curve (before jitter): 5s, 10s, 20s, 40s, 80s, 160s, then 5min for
// every remaining attempt. Total budget across 10 attempts is roughly
// 5+10+20+40+80+160 + 3*300 = ~20 minutes of sustained failure
// tolerated before declaring the error terminal. Context cancellation
// (user stop, shutdown) still aborts immediately.
func DefaultPolicy() Policy {
	return Policy{
		MaxAttempts: 10,
		BaseDelay:   5 * time.Second,
		MaxDelay:    5 * time.Minute,
		Multiplier:  2.0,
		Jitter:      0.2,
	}
}

// Event reports an attempt's outcome. A success event has Err=nil and
// Final=true. An in-between failure has Err!=nil and NextDelay>0. An
// exhausted or non-retryable failure has Err!=nil and Final=true.
type Event struct {
	Attempt     int           // 1-indexed. 1 = first try, 2 = first retry, etc.
	MaxAttempts int           // from Policy, for display like "3/10"
	Err         error         // nil on success
	NextDelay   time.Duration // wait before next attempt. 0 if Final.
	Final       bool          // true if no further attempts will be made
}

// Do runs op under the policy. onEvent may be nil. Returns op's last
// error (or nil on success, or ctx.Err on cancellation).
func Do(ctx context.Context, policy Policy, onEvent func(Event), op func(ctx context.Context) error) error {
	if policy.MaxAttempts < 1 {
		policy.MaxAttempts = 1
	}
	if policy.Multiplier <= 0 {
		policy.Multiplier = 2.0
	}
	if policy.BaseDelay <= 0 {
		policy.BaseDelay = time.Second
	}
	if policy.MaxDelay <= 0 {
		policy.MaxDelay = 60 * time.Second
	}

	delay := policy.BaseDelay
	var lastErr error

	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := op(ctx)
		if err == nil {
			emit(onEvent, Event{
				Attempt:     attempt,
				MaxAttempts: policy.MaxAttempts,
				Final:       true,
			})
			return nil
		}
		lastErr = err

		// Permanent error: bail without consuming more attempts.
		if !IsRetryable(err) {
			emit(onEvent, Event{
				Attempt:     attempt,
				MaxAttempts: policy.MaxAttempts,
				Err:         err,
				Final:       true,
			})
			return err
		}

		// Exhausted: last attempt was our last shot.
		if attempt == policy.MaxAttempts {
			emit(onEvent, Event{
				Attempt:     attempt,
				MaxAttempts: policy.MaxAttempts,
				Err:         err,
				Final:       true,
			})
			return err
		}

		wait := applyJitter(delay, policy.Jitter)
		emit(onEvent, Event{
			Attempt:     attempt,
			MaxAttempts: policy.MaxAttempts,
			Err:         err,
			NextDelay:   wait,
		})

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}

		next := time.Duration(float64(delay) * policy.Multiplier)
		if next > policy.MaxDelay {
			next = policy.MaxDelay
		}
		delay = next
	}
	return lastErr
}

// IsRetryable classifies an error as transient (retry) or permanent
// (don't retry). The service's sentinel errors carry most of the
// signal; for HTTP-status-encoded errors from the adapter layer we
// fall back to message inspection because the adapters don't surface
// status as a field.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	// Context cancellation is not a retry case — the caller wants out.
	if errors.Is(err, context.Canceled) {
		return false
	}
	// Deadline on the outer context is also not retryable (the deadline
	// belongs to the caller); but a per-request timeout that manifests
	// as a network error IS retryable — distinguished below.

	// Auth or config-shaped errors: never retry.
	if errors.Is(err, core.ErrAuth) {
		return false
	}

	// 429 rate-limit: yes, with backoff.
	if errors.Is(err, core.ErrRateLimited) {
		return true
	}

	// Provider unavailable covers HTTP non-200 + network failures.
	// The message carries the HTTP status or network-error text.
	if errors.Is(err, core.ErrProviderUnavailable) {
		msg := err.Error()
		// 5xx and gateway/service-unavailable phrasing: transient.
		if containsAny(msg,
			"500", "502", "503", "504",
			"Bad Gateway", "Gateway Timeout",
			"Service Unavailable", "Service Temporarily Unavailable",
			"Internal Server Error",
		) {
			return true
		}
		// Network-layer retryables.
		if containsAny(msg,
			"context deadline exceeded",
			"Client.Timeout exceeded",
			"connection refused",
			"connection reset",
			"no such host",
			"unexpected EOF",
			"broken pipe",
			"i/o timeout",
			"TLS handshake timeout",
		) {
			return true
		}
		// 4xx status codes that aren't auth are permanent (bad request,
		// not found, unprocessable entity, etc.). Don't retry those —
		// they won't fix themselves.
		if containsAny(msg, "400", "404", "409", "410", "422") {
			return false
		}
		// Default for ErrProviderUnavailable when we can't classify:
		// treat as transient. The name of the sentinel implies it.
		return true
	}

	// Anything else uncategorized: don't retry. Retryability requires
	// explicit evidence.
	return false
}

// --- helpers ---

func emit(cb func(Event), e Event) {
	if cb != nil {
		cb(e)
	}
}

func applyJitter(d time.Duration, fraction float64) time.Duration {
	if fraction <= 0 {
		return d
	}
	if fraction > 1 {
		fraction = 1
	}
	// uniform in [1-f, 1+f]
	delta := (rand.Float64()*2 - 1) * fraction
	return time.Duration(float64(d) * (1 + delta))
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
