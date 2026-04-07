package tei

import (
	"context"
	"math"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/internal/httpc"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// CompositeClassifier combines NLI classification with embedding cosine
// similarity. Returns the stronger dependency signal for each pair.
// The Classifier interface is satisfied — the engine doesn't know or care
// that multiple models contribute to the score.
type CompositeClassifier struct {
	nli      *classifier // TEI /predict (NLI model)
	embedder *embedder   // TEI /embed (embedding model)
}

// NewCompositeClassifier creates a classifier that fuses NLI entailment
// with embedding similarity. nliURL serves a cross-encoder NLI model,
// embedURL serves an embedding model. Either can be empty to disable.
func NewCompositeClassifier(nliURL, embedURL string) core.Classifier {
	cc := &CompositeClassifier{}
	if nliURL != "" {
		cc.nli = &classifier{
			baseURL: nliURL,
			client:  httpc.New(httpc.TimeoutDefault, nil),
		}
	}
	if embedURL != "" {
		cc.embedder = &embedder{
			baseURL: embedURL,
			client:  httpc.New(httpc.TimeoutDefault, nil),
		}
	}
	return cc
}

func (c *CompositeClassifier) Classify(ctx context.Context, req *pb.ClassifyRequest) (*pb.ClassifyResponse, error) {
	var nliScore, simScore float64

	// NLI signal
	if c.nli != nil {
		resp, err := c.nli.Classify(ctx, req)
		if err == nil {
			for _, l := range resp.Labels {
				if l.Name == "entailment" {
					nliScore = float64(l.Probability)
					break
				}
			}
		}
	}

	// Embedding similarity signal
	if c.embedder != nil {
		vecA, errA := c.embedder.Embed(ctx, req.TextA)
		vecB, errB := c.embedder.Embed(ctx, req.TextB)
		if errA == nil && errB == nil {
			simScore = cosine(vecA, vecB)
		}
	}

	// Take the max — whichever signal is stronger for this pair
	score := math.Max(nliScore, simScore)

	return &pb.ClassifyResponse{
		Labels: []*pb.ClassLabel{
			{Name: "entailment", Probability: float32(score)},
			{Name: "neutral", Probability: float32(1 - score)},
			{Name: "contradiction", Probability: 0},
		},
	}, nil
}

func (c *CompositeClassifier) ClassifyBatch(ctx context.Context, req *pb.BatchClassifyRequest) (*pb.BatchClassifyResponse, error) {
	results := make([]*pb.ClassifyResponse, len(req.Pairs))
	for i, pair := range req.Pairs {
		resp, err := c.Classify(ctx, pair)
		if err != nil {
			return nil, err
		}
		results[i] = resp
	}
	return &pb.BatchClassifyResponse{Results: results}, nil
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
