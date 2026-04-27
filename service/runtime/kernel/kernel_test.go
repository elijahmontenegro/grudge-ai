package kernel

import (
	"context"
	"path/filepath"
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
