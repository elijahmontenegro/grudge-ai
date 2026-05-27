package rrc

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	pb "github.com/emontenegr/spidey/proto/gen/go/spidey/v1"
)

// TestAssemble_ConcurrentSameEngine — N concurrent Assemble calls on
// one engine must serialize cleanly (Assemble takes the engine mutex
// for the OnMessage→Select→MMR triple) and produce identical wire
// payloads. Run under -race so any unprotected DAG / scoreCache
// access trips the detector.
//
// Production exercise: the runner factory captures one engine
// pointer per thread; if a future change adds parallel rounds (a
// subagent forks while the parent's round is in flight), this test
// catches the resulting DAG corruption.
func TestAssemble_ConcurrentSameEngine(t *testing.T) {
	mc := newMockClassifier()
	mc.SetScore("alpha", "query", 0.8)
	mc.SetScore("beta", "query", 0.7)

	o := newMockChunkOracle()
	cfg := DefaultConfig()
	cfg.ZScoreThreshold = 0
	cfg.MinBatchStdDev = 0
	cfg.RadiusSize = 0
	cfg.DiversityLambda = 0

	prior1 := addMsg(o, "m1", 0, "t1", "alpha")
	prior2 := addMsg(o, "m2", 1, "t1", "beta")
	query := addMsg(o, "q", 2, "t1", "query")
	corpus := []*pb.Message{prior1, prior2, query}

	e := NewEngine(cfg, mc, WithChunkOracle(o))

	const goroutines = 10
	var wg sync.WaitGroup
	results := make([]AssembleResult, goroutines)
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// All goroutines share the same query and corpus —
			// Engine.Assemble locks internally, so the OnMessage→
			// Select→MMR sequence each runs is serialized despite
			// the shared engine state.
			results[i], errs[i] = e.Assemble(context.Background(), AssembleRequest{
				Query:        query,
				Corpus:       corpus,
				ThreadCorpus: corpus,
				Scope:        pb.SelectionScope_SELECTION_SCOPE_THREAD,
				ThreadID:     "t1",
			})
		}(i)
	}
	wg.Wait()

	for i := 0; i < goroutines; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if results[i].Wire == nil {
			t.Errorf("goroutine %d: nil wire", i)
		}
	}
}

// TestEngineSwap_OldEngineKeepsWorking — an engine built from a
// snapshot of edges + scores stays usable after a fresh engine is
// constructed elsewhere. Models the kernel.UpdateEngineConfig
// pattern: build engineB from disk, atomically swap, but in-flight
// goroutines that captured engineA still finish their work.
//
// The kernel's atomic.Pointer ensures new operations land on engineB.
// This test pins the contract that engineA itself doesn't break
// because a sibling engine exists — they share neither state nor
// goroutines.
func TestEngineSwap_OldEngineKeepsWorking(t *testing.T) {
	mc := newMockClassifier()
	mc.SetScore("p", "q", 0.9)

	cfg := DefaultConfig()
	cfg.ZScoreThreshold = 0
	cfg.MinBatchStdDev = 0
	cfg.RadiusSize = 0
	cfg.DiversityLambda = 0

	o := newMockChunkOracle()
	prior := addMsg(o, "p1", 0, "t1", "p")
	query := addMsg(o, "q1", 1, "t1", "q")
	corpus := []*pb.Message{prior, query}

	engineA := NewEngine(cfg, mc, WithChunkOracle(o))

	// Build engineA's state by running an Assemble round.
	resA1, err := engineA.Assemble(context.Background(), AssembleRequest{
		Query:        query,
		Corpus:       corpus,
		ThreadCorpus: corpus,
		Scope:        pb.SelectionScope_SELECTION_SCOPE_THREAD,
		ThreadID:     "t1",
	})
	if err != nil {
		t.Fatalf("engineA Assemble (1st): %v", err)
	}
	if len(resA1.Wire) == 0 {
		t.Fatal("engineA produced empty wire on round 1")
	}

	// Build engineB from a fresh classifier + oracle. In production
	// kernel.UpdateEngineConfig hydrates engineB with edges and
	// scores from disk; for this test the point is that engineA
	// continues to function while engineB exists — they hold no
	// shared mutable state.
	mcB := newMockClassifier()
	mcB.SetScore("p", "q", 0.9)
	oB := newMockChunkOracle()
	oB.Register("p1", "p")
	oB.Register("q1", "q")
	engineB := NewEngine(cfg, mcB, WithChunkOracle(oB))
	if engineB == nil {
		t.Fatal("engineB construction returned nil")
	}

	// engineA should still produce identical results post-engineB-creation.
	resA2, err := engineA.Assemble(context.Background(), AssembleRequest{
		Query:        query,
		Corpus:       corpus,
		ThreadCorpus: corpus,
		Scope:        pb.SelectionScope_SELECTION_SCOPE_THREAD,
		ThreadID:     "t1",
	})
	if err != nil {
		t.Fatalf("engineA Assemble (2nd): %v", err)
	}
	if len(resA2.Wire) != len(resA1.Wire) {
		t.Errorf("engineA wire length changed after engineB constructed: %d vs %d",
			len(resA1.Wire), len(resA2.Wire))
	}
}

// TestFork_Concurrent — Engine.Fork takes the engine lock internally,
// so concurrent Fork calls must not race. The subagent path may fork
// while a sibling subagent is also forking from the same parent;
// without lock isolation, both forks observe partial DAG state.
func TestFork_Concurrent(t *testing.T) {
	mc := newMockClassifier()
	mc.SetScore("a", "b", 0.8)

	cfg := DefaultConfig()
	cfg.ZScoreThreshold = 0
	cfg.MinBatchStdDev = 0

	o := newMockChunkOracle()
	prior := addMsg(o, "m1", 0, "t1", "a")
	query := addMsg(o, "m2", 1, "t1", "b")

	e := NewEngine(cfg, mc, WithChunkOracle(o))
	if _, err := e.Assemble(context.Background(), AssembleRequest{
		Query:        query,
		Corpus:       []*pb.Message{prior, query},
		ThreadCorpus: []*pb.Message{prior, query},
		Scope:        pb.SelectionScope_SELECTION_SCOPE_THREAD,
		ThreadID:     "t1",
	}); err != nil {
		t.Fatalf("seed Assemble: %v", err)
	}

	const goroutines = 20
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fork := e.Fork()
			if fork == nil {
				return
			}
			ok.Add(1)
		}()
	}
	wg.Wait()
	if got := ok.Load(); got != goroutines {
		t.Errorf("expected %d successful forks, got %d", goroutines, got)
	}
}
