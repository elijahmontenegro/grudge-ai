package core

import (
	"context"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// Classifier performs pairwise text classification (cross-encoder pattern).
// Takes two texts, returns class probabilities. Batch failure is all-or-nothing.
// Implementations must be safe for concurrent use.
type Classifier interface {
	Classify(ctx context.Context, req *pb.ClassifyRequest) (*pb.ClassifyResponse, error)
	ClassifyBatch(ctx context.Context, req *pb.BatchClassifyRequest) (*pb.BatchClassifyResponse, error)
}
