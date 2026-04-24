package resilience

import (
	"context"

	"github.com/emontenegr/spidey/core"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// RetryingCompleter wraps any core.Completer with the retry policy.
// Pre-stream failures (handshake, 5xx before body) are retried. Once a
// stream starts producing bytes we can't meaningfully retry — a mid-
// stream failure surfaces as a single chunk with Error set, no retry.
// Complete() is fully re-runnable and gets the full retry treatment.
type RetryingCompleter struct {
	Inner   core.Completer
	Policy  Policy
	OnEvent func(Event) // may be nil
}

// NewRetryingCompleter wraps inner. If policy is the zero value, the
// DefaultPolicy is used.
func NewRetryingCompleter(inner core.Completer, policy Policy, onEvent func(Event)) *RetryingCompleter {
	if policy.MaxAttempts == 0 {
		policy = DefaultPolicy()
	}
	return &RetryingCompleter{Inner: inner, Policy: policy, OnEvent: onEvent}
}

// Complete retries end-to-end. Non-retryable errors surface on the
// first attempt without eating the retry budget.
func (r *RetryingCompleter) Complete(ctx context.Context, req *pb.CompletionRequest) (*pb.CompletionResponse, error) {
	var resp *pb.CompletionResponse
	err := Do(ctx, r.Policy, r.OnEvent, func(ctx context.Context) error {
		var e error
		resp, e = r.Inner.Complete(ctx, req)
		return e
	})
	return resp, err
}

// Stream retries the handshake phase — the call that either returns a
// chunk channel or errors before the first byte. Once the channel is
// returned we assume the stream started; subsequent errors come as
// chunk.Error and are the caller's to surface (partial output has
// already been consumed upstream so we can't replay).
func (r *RetryingCompleter) Stream(ctx context.Context, req *pb.CompletionRequest) (<-chan *pb.StreamChunk, error) {
	var ch <-chan *pb.StreamChunk
	err := Do(ctx, r.Policy, r.OnEvent, func(ctx context.Context) error {
		var e error
		ch, e = r.Inner.Stream(ctx, req)
		return e
	})
	return ch, err
}
