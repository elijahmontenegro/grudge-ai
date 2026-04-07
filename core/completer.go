package core

import (
	"context"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// Completer provides language model completion. Stream sets stream=true on the
// wire regardless of the proto field value; Complete sets stream=false.
// Implementations must be safe for concurrent use.
type Completer interface {
	Complete(ctx context.Context, req *pb.CompletionRequest) (*pb.CompletionResponse, error)
	Stream(ctx context.Context, req *pb.CompletionRequest) (<-chan *pb.StreamChunk, error)
}
