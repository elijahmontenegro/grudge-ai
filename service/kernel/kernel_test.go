package kernel

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/substrate"
	"github.com/emontenegr/spidey/service/storage"
)

// TestBootstrap_Minimal — kernel.Bootstrap with no providers
// configured returns a usable kernel: storage open, engine
// non-nil, runners empty. Substrate logs warnings about missing
// providers but does not fail.
//
// The provider-less mode is what FirstRun ships with — settings
// page renders, the user configures providers, then ReloadProviders
// rotates the engine in. Bootstrap not erroring on that state is
// what keeps FirstRun reachable.
func TestBootstrap_Minimal(t *testing.T) {
	dir := t.TempDir()
	cfg := minimalConfig(dir)

	k, err := Bootstrap(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() {
		if err := k.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	}()

	if k.DB == nil {
		t.Error("DB nil after Bootstrap")
	}
	if k.Engine() == nil {
		t.Error("Engine() nil after Bootstrap")
	}
	if k.Runners == nil {
		t.Error("Runners nil after Bootstrap")
	}
}

// TestBootstrap_ShutdownRebootstrap_PreservesAgentState — the
// kernel-restart contract from the verification plan: write agent
// state, shutdown, re-bootstrap, and the row is still readable.
// Catches storage-level boot bugs (DSN drift, schema migration
// not idempotent) and a class of "kernel forgets the world" bugs
// the previous resolver-state design was prone to.
func TestBootstrap_ShutdownRebootstrap_PreservesAgentState(t *testing.T) {
	dir := t.TempDir()
	cfg := minimalConfig(dir)
	threadID := "thread-1"

	// First boot: open kernel, persist a thread + agent state row,
	// confirm the read-back, then shut down.
	k, err := Bootstrap(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Bootstrap (1st): %v", err)
	}
	if err := k.DB.CreateThread(&pb.Thread{Id: threadID, Name: "first thread"}); err != nil {
		k.Shutdown()
		t.Fatalf("CreateThread: %v", err)
	}
	if err := k.DB.EnsureAgentStateRow(threadID, storage.AgentStatusPaused, storage.AgentModeAutonomous); err != nil {
		k.Shutdown()
		t.Fatalf("EnsureAgentStateRow: %v", err)
	}
	if err := k.Shutdown(); err != nil {
		t.Fatalf("Shutdown (1st): %v", err)
	}

	// Second boot against the same DataDir: row must survive.
	k2, err := Bootstrap(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Bootstrap (2nd): %v", err)
	}
	defer k2.Shutdown()

	st, err := k2.DB.GetAgentState(threadID)
	if err != nil {
		t.Fatalf("GetAgentState after rebootstrap: %v", err)
	}
	if st == nil {
		t.Fatal("agent state row missing after rebootstrap")
	}
	if st.Status != storage.AgentStatusPaused {
		t.Errorf("Status: got %v want %v", st.Status, storage.AgentStatusPaused)
	}
	if st.Mode != storage.AgentModeAutonomous {
		t.Errorf("Mode: got %v want %v", st.Mode, storage.AgentModeAutonomous)
	}
}

// TestBootstrap_EngineSwapsCleanly — UpdateEngineConfig replaces
// the engine pointer atomically without affecting other kernel
// state. Confirms the atomic-swap design retires the previous
// setter-with-external-lock pattern: callers see a fresh engine,
// runners get StopAll'd, but the DB and the kernel itself are
// untouched.
func TestBootstrap_EngineSwapsCleanly(t *testing.T) {
	dir := t.TempDir()
	cfg := minimalConfig(dir)

	k, err := Bootstrap(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer k.Shutdown()

	first := k.Engine()
	if first == nil {
		t.Fatal("engine nil before swap")
	}
	newCfg := first.Config()
	newCfg.EdgeThreshold = 0.42 // distinct value to verify the swap

	if err := k.UpdateEngineConfig(context.Background(), newCfg); err != nil {
		t.Fatalf("UpdateEngineConfig: %v", err)
	}

	second := k.Engine()
	if second == first {
		t.Error("engine pointer unchanged after UpdateEngineConfig — swap failed")
	}
	if second.Config().EdgeThreshold != 0.42 {
		t.Errorf("new engine missing the new config: EdgeThreshold=%v want 0.42",
			second.Config().EdgeThreshold)
	}
}

// TestKernel_EngineSwap_AtomicUnderConcurrentReads — many readers
// spinning on k.Engine() while a writer rotates the engine via
// UpdateEngineConfig. atomic.Pointer guarantees no torn reads;
// this test runs under -race so any unprotected pointer
// publication or stale-write trips the detector.
//
// The mid-Assemble-with-real-classifier scenario is env-bound (needs
// a registered fake provider), but the kernel-side correctness — that
// readers never see a half-published pointer or a nil engine during
// the swap — is the part that's testable here, and it's the part
// most likely to regress if a future change replaces atomic.Pointer
// with a non-atomic scheme.
func TestKernel_EngineSwap_AtomicUnderConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	cfg := minimalConfig(dir)

	k, err := Bootstrap(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer k.Shutdown()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	const readers = 8
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					e := k.Engine()
					if e == nil {
						t.Error("Engine() returned nil during swap")
						return
					}
					// Read a field — exercises both the pointer
					// swap and any reads against the engine state
					// that were captured at the moment of read.
					_ = e.Config().EdgeThreshold
				}
			}
		}()
	}

	// Writer: rotate the engine N times.
	const swaps = 50
	for i := 0; i < swaps; i++ {
		newCfg := k.Engine().Config()
		newCfg.EdgeThreshold = float64(i+1) / 100.0
		if err := k.UpdateEngineConfig(context.Background(), newCfg); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("UpdateEngineConfig (iter %d): %v", i, err)
		}
	}
	close(stop)
	wg.Wait()

	// Final pointer carries the last config we wrote.
	final := k.Engine().Config()
	want := float64(swaps) / 100.0
	if diff := final.EdgeThreshold - want; diff < -1e-9 || diff > 1e-9 {
		t.Errorf("final EdgeThreshold: got %v want %v", final.EdgeThreshold, want)
	}
}

// TestKernel_MidAssembleEngineSwap — Goroutine A starts an Assemble
// against the engine kernel published at boot; while A is blocked
// inside the slow Score call, UpdateEngineConfig fires and rebuilds
// substrate with a different classifier injected. A completes on
// the engine it captured, B sees the new engine. No panic, no torn
// write to the old engine's DAG.
//
// Goes through the production path: Bootstrap with substrate.WithClassifier
// at boot, UpdateEngineConfig with substrate.WithClassifier on swap.
// substrate.Build constructs the engine in both cases via the same
// code path; the test exercises the actual production swap, not a
// shortcut. Validates atomic.Pointer correctness AND substrate's
// rebuild-and-swap behavior under concurrent in-flight Assemble.
func TestKernel_MidAssembleEngineSwap(t *testing.T) {
	dir := t.TempDir()
	cfg := minimalConfig(dir)
	cfg.Settings.Engine = config.EngineConfig{
		EdgeThreshold:   0.5,
		ScoreFloor:      0.0,
		ZScoreThreshold: 0,
		MinBatchStdDev:  0,
		RadiusSize:      0,
		RerankTopK:      32,
	}

	scorerA := &gatedScorer{enter: make(chan struct{}, 1), release: make(chan struct{})}
	oracleA := &fakeOracle{texts: map[string]string{
		"prior": "alpha",
		"query": "beta",
	}}

	k, err := Bootstrap(
		context.Background(), cfg,
		substrate.WithClassifier(scorerA),
		substrate.WithChunkOracle(oracleA),
	)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer k.Shutdown()

	prior := makeFakeMsg("prior", 0, "t1", "alpha")
	query := makeFakeMsg("query", 1, "t1", "beta")
	corpus := []*pb.Message{prior, query}

	type asmResult struct {
		res rrc.AssembleResult
		err error
	}
	resCh := make(chan asmResult, 1)
	engineA := k.Engine()
	engineAThreshold := engineA.Config().EdgeThreshold // capture before swap mutates cfg

	// Goroutine A captures engineA via k.Engine() and starts
	// Assemble. The gated classifier signals on `enter` once Score
	// is reached, then blocks on `release` until the test allows
	// it to return.
	go func() {
		e := k.Engine()
		res, err := e.Assemble(context.Background(), rrc.AssembleRequest{
			Query:        query,
			Corpus:       corpus,
			ThreadCorpus: corpus,
			Scope:        pb.SelectionScope_SELECTION_SCOPE_THREAD,
			ThreadID:     "t1",
		})
		resCh <- asmResult{res: res, err: err}
	}()

	select {
	case <-scorerA.enter:
	case <-time.After(2 * time.Second):
		t.Fatal("scorer A never reached Score()")
	}

	// Mid-Assemble: drive UpdateEngineConfig with a different
	// classifier and oracle. substrate.Build rebuilds, atomic.Store
	// publishes the new engine. engineA is still alive, A still
	// holds it.
	scorerB := &gatedScorer{enter: make(chan struct{}, 1), release: make(chan struct{})}
	oracleB := &fakeOracle{texts: map[string]string{
		"prior": "alpha",
		"query": "beta",
	}}
	newCfg := engineA.Config()
	newCfg.EdgeThreshold = 0.42 // distinct so we can verify the swap
	if err := k.UpdateEngineConfig(
		context.Background(), newCfg,
		substrate.WithClassifier(scorerB),
		substrate.WithChunkOracle(oracleB),
	); err != nil {
		close(scorerA.release)
		t.Fatalf("UpdateEngineConfig: %v", err)
	}

	if engineA == k.Engine() {
		close(scorerA.release)
		t.Fatal("engine pointer unchanged after UpdateEngineConfig — swap failed")
	}
	if k.Engine().Config().EdgeThreshold != 0.42 {
		t.Errorf("new engine missing new config: EdgeThreshold=%v want 0.42",
			k.Engine().Config().EdgeThreshold)
	}

	// Release A. It completes on engineA — the engine it captured
	// before the swap.
	close(scorerA.release)
	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("goroutine A Assemble failed mid-swap: %v", r.err)
		}
		if len(r.res.Wire) == 0 {
			t.Error("goroutine A produced empty wire")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine A did not complete after release")
	}

	// engineA's config remains intact post-swap. UpdateEngineConfig
	// mutated cfg.Settings.Engine in place, so we compare against
	// the threshold we captured before the swap.
	if engineA.Config().EdgeThreshold != engineAThreshold {
		t.Errorf("engineA config corrupted: EdgeThreshold=%v want %v",
			engineA.Config().EdgeThreshold, engineAThreshold)
	}

	// scorerB hasn't been called yet — A's release didn't wake the
	// wrong gated channel, confirming the engines hold no shared
	// goroutines.
	if scorerB.callCount.Load() != 0 {
		t.Errorf("scorerB should not have been called yet, got %d calls",
			scorerB.callCount.Load())
	}
}

// gatedScorer is a Scorer that signals on `enter` when Score is
// invoked, then blocks until `release` closes. The first call also
// records itself in callCount for later assertions.
type gatedScorer struct {
	enter     chan struct{}
	release   chan struct{}
	callCount atomic.Int32
}

func (g *gatedScorer) Score(_ context.Context, _ string, candidates []string) ([]float64, error) {
	g.callCount.Add(1)
	select {
	case g.enter <- struct{}{}:
	default:
	}
	<-g.release
	out := make([]float64, len(candidates))
	for i := range out {
		out[i] = 0.7
	}
	return out, nil
}

// fakeOracle is a minimal ChunkOracle: each registered text is
// returned as a single chunk at index 0 with a fixed unit vector
// so cosine prefilter is a no-op.
type fakeOracle struct {
	texts map[string]string
}

func (o *fakeOracle) ChunksForMessages(_ context.Context, ids []string) (map[string][]rrc.ChunkRef, error) {
	out := make(map[string][]rrc.ChunkRef)
	for _, id := range ids {
		if t, ok := o.texts[id]; ok {
			out[id] = []rrc.ChunkRef{{
				MessageID: id, ChunkIndex: 0, Text: t, Vector: []float32{1.0},
			}}
		}
	}
	return out, nil
}

func (o *fakeOracle) EnsureVector(_ context.Context, _ rrc.ChunkRef) ([]float32, error) {
	return []float32{1.0}, nil
}

func (o *fakeOracle) NearestChunks(_ context.Context, _ string, k int, _ rrc.Predicate) ([]rrc.ChunkRef, error) {
	out := make([]rrc.ChunkRef, 0, len(o.texts))
	for id, t := range o.texts {
		out = append(out, rrc.ChunkRef{
			MessageID: id, ChunkIndex: 0, Text: t, Vector: []float32{1.0},
		})
	}
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// DiversityRerank: pass-through. This fakeOracle only exists to drive
// engine hot-swap tests; the diversity behavior is covered in rrc's
// own test suite against vectorOracle.
func (o *fakeOracle) DiversityRerank(_ context.Context, candidates []*pb.SelectedMessage, _ map[string]float64, _ float64) ([]*pb.SelectedMessage, error) {
	return candidates, nil
}

// makeFakeMsg constructs a minimal message for the swap test.
func makeFakeMsg(id string, position int64, threadID, text string) *pb.Message {
	return &pb.Message{
		Id:       id,
		Role:     pb.Role_ROLE_USER,
		Content:  []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: text}}}},
		Position: position,
		ThreadId: threadID,
	}
}

// minimalConfig returns a Config with no providers, no MCP servers,
// no skills directory — the minimum substrate Bootstrap can
// successfully wire. DataDir is t.TempDir() so each test gets a
// fresh SQLite file.
func minimalConfig(dir string) *config.Config {
	return &config.Config{
		Paths: config.Paths{
			DataDir:   dir,
			ConfigDir: filepath.Join(dir, "config"),
			CacheDir:  filepath.Join(dir, "cache"),
		},
		Settings: config.Settings{
			Providers: map[string]config.ProviderConfig{},
		},
	}
}
