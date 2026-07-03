package rrc

import (
	"context"
	"testing"

	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/proto/pbtext"
	"github.com/elijahmontenegro/grudge/rrc/chunk"
)

// charEstimator is a deterministic 4-chars-per-token estimator. The
// engine's hot path needs *some* estimator wired; we don't depend on
// exact tiktoken numerics here, just on the count being stable
// across runs.
type charEstimator struct{}

func (charEstimator) Estimate(text string) int { return (len(text) + 3) / 4 }

// testChunkConfig is chunk.DefaultConfig with the deterministic test
// estimator installed (the estimator now lives on Config, not a global).
func testChunkConfig() chunk.Config {
	cfg := chunk.DefaultConfig()
	cfg.Estimator = charEstimator{}
	return cfg
}

// TestAssemble_Deterministic — fixed corpus + fixed scorer outputs
// + fixed config produce identical wire payloads across runs.
// Engine.Assemble has no random sources internally (DAG sort is
// stable, MMR is greedy-deterministic, score cache is keyed by
// content); the test pins that contract so a future change that
// introduces non-determinism (random tie-breaking, time-dependent
// ordering, map iteration leak) trips here.
func TestAssemble_Deterministic(t *testing.T) {
	mc := newMockScorer()
	mc.SetScore("alpha", "current context", 0.8)
	mc.SetScore("beta", "current context", 0.7)
	mc.SetScore("gamma", "current context", 0.6)

	o := newMockChunkOracle()

	cfg := DefaultConfig()
	cfg.Chunk.Estimator = charEstimator{}
	cfg.MinBatchStdDev = 0
	cfg.LocalContextSize = 1
	cfg.DiversityLambda = 0 // disable MMR — focus the test on Selection

	prior1 := addMsg(o, "m1", 0, "t1", "alpha")
	prior2 := addMsg(o, "m2", 1, "t1", "beta")
	prior3 := addMsg(o, "m3", 2, "t1", "gamma")
	anchor := addMsg(o, "q", 3, "t1", "current context")
	corpus := []*threadv1.Message{prior1, prior2, prior3, anchor}

	first := runAssemble(t, cfg, mc, o, corpus, anchor)
	second := runAssemble(t, cfg, mc, o, corpus, anchor)

	if len(first.Wire) != len(second.Wire) {
		t.Fatalf("wire length differs: first=%d second=%d", len(first.Wire), len(second.Wire))
	}
	for i := range first.Wire {
		ft := pbtext.TextFromBlocks(first.Wire[i].Content)
		st := pbtext.TextFromBlocks(second.Wire[i].Content)
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
func runAssemble(t *testing.T, cfg EngineConfig, mc *mockScorer, o *mockChunkOracle, corpus []*threadv1.Message, anchor *threadv1.Message) AssembleResult {
	t.Helper()
	e := NewEngine(cfg, mc, WithChunkOracle(o))
	res, err := e.Assemble(context.Background(), AssembleRequest{
		SerializedLocalContext: testSerializedLocalContext(anchor),
		Anchor:                 anchor,
		Corpus:                 corpus,
		LocalContext:           []*threadv1.Message{anchor},
		Scope:                  threadv1.SelectionScope_SELECTION_SCOPE_THREAD,
		ThreadID:               "t1",
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return res
}
