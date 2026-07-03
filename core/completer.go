package core

import (
	"context"
	"iter"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
)

// Completer provides language model completion. Stream sets
// stream=true on the wire regardless of the proto field value;
// Complete sets stream=false. Implementations must be safe for
// concurrent use.
//
// Stream returns iter.Seq2 — the consumer ranges over (chunk, err)
// pairs. Pre-stream errors (handshake failure, 4xx/5xx before the
// first chunk) yield as a single (nil, err) pair followed by the
// iterator returning. Mid-stream errors yield (nil, err) and end
// the iteration. Goroutine lifecycle is bound to the iteration —
// breaking out of the range stops production.
type Completer interface {
	Complete(ctx context.Context, req *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error)
	Stream(ctx context.Context, req *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error]
}
