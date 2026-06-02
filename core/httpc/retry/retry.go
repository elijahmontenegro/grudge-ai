// Package retry provides bounded-attempt retry with exponential
// backoff, jitter, and a progress callback the service can bridge
// into user-facing UI events. The intent is principled retry, not
// silent sleep-retry: every attempt reports an Event so the caller
// can render "retrying 2/10, next attempt in 8s (503 Service
// Unavailable)" to the user. Context cancellation aborts retries
// immediately.
//
// The classifier IsRetryable looks for any error implementing
// `interface{ Status() int }` (duck-typed against the concrete
// http.StatusError shape, plus any adapter-defined error that
// wraps a status). Falls back to checking core sentinels
// (ErrAuth, ErrRateLimited, ErrProviderUnavailable) and
// transport-error string patterns for pre-typed-error code paths.
package retry

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/elijahmontenegro/grudge/core"
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
// range.
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

// Event reports an attempt's outcome. A success event has Err=nil
// and Final=true. An in-between failure has Err!=nil and
// NextDelay>0. An exhausted or non-retryable failure has Err!=nil
// and Final=true.
type Event struct {
	Attempt     int           // 1-indexed. 1 = first try, 2 = first retry, etc.
	MaxAttempts int           // from Policy, for display like "3/10"
	Err         error         // nil on success
	NextDelay   time.Duration // wait before next attempt. 0 if Final.
	Final       bool          // true if no further attempts will be made
}

// Do runs op under the policy. onEvent may be nil. Returns op's
// last error (or nil on success, or ctx.Err on cancellation).
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

		if !IsRetryable(err) {
			emit(onEvent, Event{
				Attempt:     attempt,
				MaxAttempts: policy.MaxAttempts,
				Err:         err,
				Final:       true,
			})
			return err
		}

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

// IsRetryable classifies an error as transient or permanent. The
// preference order is:
//
//  1. Context cancellation / deadline → not retryable (caller wants out).
//  2. Any error exposing `Status() int` (duck-typed) → retry iff
//     the status is 408, 429, or 5xx. Decoupled from any concrete
//     error type so adapter-defined status carriers classify the
//     same as core/internal/httpc.StatusError.
//  3. core.ErrAuth → not retryable (config issue).
//  4. core.ErrRateLimited → retryable.
//  5. core.ErrProviderUnavailable → message-pattern classification:
//     5xx / gateway / network errors → retryable;
//     4xx codes → not retryable.
//  6. Anything else → not retryable (require explicit evidence).
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var sc interface{ Status() int }
	if errors.As(err, &sc) {
		return statusRetryable(sc.Status())
	}

	if errors.Is(err, core.ErrAuth) {
		return false
	}
	if errors.Is(err, core.ErrRateLimited) {
		return true
	}
	if errors.Is(err, core.ErrProviderUnavailable) {
		msg := err.Error()
		if containsAny(msg,
			"500", "502", "503", "504",
			"Bad Gateway", "Gateway Timeout",
			"Service Unavailable", "Service Temporarily Unavailable",
			"Internal Server Error",
		) {
			return true
		}
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
		if containsAny(msg, "400", "404", "409", "410", "422") {
			return false
		}
		return true
	}

	return false
}

// statusRetryable applies the retry policy's view of which HTTP
// codes are worth a second attempt. Encoded here, not on the error
// type itself, so retry owns the "what's transient" policy and
// httpc.StatusError just carries the data.
func statusRetryable(code int) bool {
	switch {
	case code == http.StatusRequestTimeout, code == http.StatusTooManyRequests:
		return true
	case code >= 500 && code < 600:
		return true
	default:
		return false
	}
}

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
