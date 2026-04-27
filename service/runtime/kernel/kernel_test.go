package kernel

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/service/config"
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
