package rrc

import (
	"context"
	"testing"

	pb "github.com/emontenegr/grudge/proto/gen/go/grudge/v1"
	"github.com/emontenegr/grudge/rrc/chunk"
)

// charEstimator is a deterministic 4-chars-per-token estimator. The
// engine's hot path needs *some* estimator wired; we don't depend on
// exact tiktoken numerics here, just on the count being stable
// across runs.
type charEstimator struct{}

func (charEstimator) Estimate(text string) int { return (len(text) + 3) / 4 }

func init() { chunk.SetDefaultEstimator(charEstimator{}) }

// TestAssemble_Deterministic — fixed corpus + fixed scorer outputs
// + fixed config produce identical wire payloads across runs.
// Engine.Assemble has no random sources internally (DAG sort is
// stable, MMR is greedy-deterministic, score cache is keyed by
// content); the test pins that contract so a future change that
// introduces non-determinism (random tie-breaking, time-dependent
// ordering, map iteration leak) trips here.
func TestAssemble_Deterministic(t *testing.T) {
	mc := newMockScorer()
	mc.SetScore("alpha", "query content", 0.8)
	mc.SetScore("beta", "query content", 0.7)
	mc.SetScore("gamma", "query content", 0.6)

	o := newMockChunkOracle()

	cfg := DefaultConfig()
	cfg.ZScoreThreshold = 0
	cfg.MinBatchStdDev = 0
	cfg.RadiusSize = 0
	cfg.DiversityLambda = 0 // disable MMR — focus the test on Selection

	prior1 := addMsg(o, "m1", 0, "t1", "alpha")
	prior2 := addMsg(o, "m2", 1, "t1", "beta")
	prior3 := addMsg(o, "m3", 2, "t1", "gamma")
	query := addMsg(o, "q", 3, "t1", "query content")
	corpus := []*pb.Message{prior1, prior2, prior3, query}

	first := runAssemble(t, cfg, mc, o, corpus, query)
	second := runAssemble(t, cfg, mc, o, corpus, query)

	if len(first.Wire) != len(second.Wire) {
		t.Fatalf("wire length differs: first=%d second=%d", len(first.Wire), len(second.Wire))
	}
	for i := range first.Wire {
		ft := TextFromBlocks(first.Wire[i].Content)
		st := TextFromBlocks(second.Wire[i].Content)
		if ft != st {
			t.Errorf("wire[%d] differs across runs:\n  first:  %q\n  second: %q", i, ft, st)
		}
		if first.Wire[i].Role != second.Wire[i].Role {
			t.Errorf("wire[%d] role differs: first=%v second=%v",
				i, first.Wire[i].Role, second.Wire[i].Role)
		}
	}

	if first.Telemetry.SelectedCount != second.Telemetry.SelectedCount {
		t.Errorf("SelectedCount differs: first=%d second=%d",
			first.Telemetry.SelectedCount, second.Telemetry.SelectedCount)
	}
	if first.Telemetry.TotalTokens != second.Telemetry.TotalTokens {
		t.Errorf("TotalTokens differs: first=%d second=%d",
			first.Telemetry.TotalTokens, second.Telemetry.TotalTokens)
	}
}

// runAssemble builds a fresh engine, scores the corpus, and returns
// the assemble output. Each call constructs a new engine — the
// determinism test compares cross-engine outputs, which is the
// stricter contract (state from one call mustn't leak into another).
func runAssemble(t *testing.T, cfg EngineConfig, mc *mockScorer, o *mockChunkOracle, corpus []*pb.Message, query *pb.Message) AssembleResult {
	t.Helper()
	e := NewEngine(cfg, mc, WithChunkOracle(o))
	res, err := e.Assemble(context.Background(), AssembleRequest{
		Query:        query,
		Corpus:       corpus,
		ThreadCorpus: corpus,
		Scope:        pb.SelectionScope_SELECTION_SCOPE_THREAD,
		ThreadID:     "t1",
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return res
}
