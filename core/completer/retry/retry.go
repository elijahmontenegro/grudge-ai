// Package retry provides a Completer decorator that wraps any
// core.Completer with the bounded-attempt retry policy from
// core/retry. Pre-stream failures (handshake, 5xx before body) are
// retried; once a stream starts producing bytes we can't replay,
// so a mid-stream failure surfaces as a single chunk with Error
// set, no retry. Complete() is fully re-runnable and gets the full
// retry treatment.
package retry

import (
	"context"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/retry"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// Completer wraps any core.Completer with the retry policy.
type Completer struct {
	Inner   core.Completer
	Policy  retry.Policy
	OnEvent func(retry.Event)
}

// New wraps inner. If policy is the zero value, retry.DefaultPolicy
// is used.
func New(inner core.Completer, policy retry.Policy, onEvent func(retry.Event)) *Completer {
	if policy.MaxAttempts == 0 {
		policy = retry.DefaultPolicy()
	}
	return &Completer{Inner: inner, Policy: policy, OnEvent: onEvent}
}

// Complete retries end-to-end. Non-retryable errors surface on the
// first attempt without consuming the retry budget.
func (c *Completer) Complete(ctx context.Context, req *pb.CompletionRequest) (*pb.CompletionResponse, error) {
	var resp *pb.CompletionResponse
	err := retry.Do(ctx, c.Policy, c.OnEvent, func(ctx context.Context) error {
		var e error
		resp, e = c.Inner.Complete(ctx, req)
		return e
	})
	return resp, err
}

// Stream retries the handshake phase — the call that either
// returns a chunk channel or errors before the first byte. Once
// the channel is returned we assume the stream started; subsequent
// errors come as chunk.Error and are the caller's to surface
// (partial output has already been consumed upstream so we can't
// replay).
func (c *Completer) Stream(ctx context.Context, req *pb.CompletionRequest) (<-chan *pb.StreamChunk, error) {
	var ch <-chan *pb.StreamChunk
	err := retry.Do(ctx, c.Policy, c.OnEvent, func(ctx context.Context) error {
		var e error
		ch, e = c.Inner.Stream(ctx, req)
		return e
	})
	return ch, err
}
