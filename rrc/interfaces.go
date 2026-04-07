package rrc

import (
	"context"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// Classifier performs pairwise text classification. Consumer-defined interface —
// any core.Classifier satisfies this via Go structural typing.
type Classifier interface {
	Classify(ctx context.Context, req *pb.ClassifyRequest) (*pb.ClassifyResponse, error)
	ClassifyBatch(ctx context.Context, req *pb.BatchClassifyRequest) (*pb.BatchClassifyResponse, error)
}

// Completer provides language model completion for QUD extraction via the small
// fast model. Consumer-defined interface — any core.Completer satisfies this.
type Completer interface {
	Complete(ctx context.Context, req *pb.CompletionRequest) (*pb.CompletionResponse, error)
}
