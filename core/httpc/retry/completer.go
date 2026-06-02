// completer.go: Completer decorator that wraps any core.Completer
// with the bounded-attempt retry policy defined in retry.go.
// Pre-stream failures (handshake, 5xx before body) are retried;
// once a stream starts producing chunks we can't replay, so a
// mid-stream failure surfaces in the iterator unchanged. Complete()
// is fully re-runnable and gets the full retry treatment.
package retry

import (
	"context"
	"iter"

	"github.com/emontenegr/grudge/core"
	pb "github.com/emontenegr/grudge/proto/gen/go/grudge/v1"
)

// Completer wraps any core.Completer with the retry policy.
type Completer struct {
	Inner   core.Completer
	Policy  Policy
	OnEvent func(Event)
}

// NewCompleter wraps inner. If policy is the zero value,
// DefaultPolicy is used.
func NewCompleter(inner core.Completer, policy Policy, onEvent func(Event)) *Completer {
	if policy.MaxAttempts == 0 {
		policy = DefaultPolicy()
	}
	return &Completer{Inner: inner, Policy: policy, OnEvent: onEvent}
}

// Complete retries end-to-end. Non-retryable errors surface on the
// first attempt without consuming the retry budget.
func (c *Completer) Complete(ctx context.Context, req *pb.CompletionRequest) (*pb.CompletionResponse, error) {
	var resp *pb.CompletionResponse
	err := Do(ctx, c.Policy, c.OnEvent, func(ctx context.Context) error {
		var e error
		resp, e = c.Inner.Complete(ctx, req)
		return e
	})
	return resp, err
}

// Stream wraps the inner Stream with handshake-phase retry. The
// inner iterator is pulled lazily; if its first emission is a
// retryable error and no chunk has been delivered yet, we abandon
// the iteration and call Stream again. Once any successful chunk
// has been yielded to the caller we can't replay — mid-stream
// errors pass through unchanged.
func (c *Completer) Stream(ctx context.Context, req *pb.CompletionRequest) iter.Seq2[*pb.StreamChunk, error] {
	return func(yield func(*pb.StreamChunk, error) bool) {
		err := Do(ctx, c.Policy, c.OnEvent, func(ctx context.Context) error {
			next, stop := iter.Pull2(c.Inner.Stream(ctx, req))
			defer stop()

			chunk, err, ok := next()
			if !ok {
				return nil // empty stream — nothing to retry
			}
			if err != nil {
				return err // handshake-phase error → retry budget
			}

			// Non-empty success — pass first chunk through, then
			// drain. Retry semantics no longer apply once we've
			// delivered output to the caller.
			if !yield(chunk, nil) {
				return nil
			}
			for {
				chunk, err, ok = next()
				if !ok {
					return nil
				}
				if !yield(chunk, err) {
					return nil
				}
			}
		})
		if err != nil {
			yield(nil, err)
		}
	}
}
