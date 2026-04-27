package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRRC_BuildsFromForeignModule — proves rrc's public API is
// reachable from a module *outside* the workspace. Creates a tmpdir
// with a fresh go.mod that uses `replace` to point at the local rrc
// path (the registry-publication scenario reduces to this case once
// gen/go publishes), writes a smoke main.go importing the engine
// surface, and runs `go build` against it.
//
// The test is the closest in-repo equivalent of the plan's
// "external `go get`" verification. The remaining gap — registry
// resolution — is bypassed by replace; once gen/go ships to a
// registry, drop the per-module replace directives and switch this
// test to use a real `go get`.
func TestRRC_BuildsFromForeignModule(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not in PATH")
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	rrcPath := filepath.Join(repoRoot, "rrc")
	genGoPath := filepath.Join(repoRoot, "gen", "go")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(`module foreign

go 1.25.0

require (
	github.com/emontenegr/spidey/gen/go v0.0.0
	github.com/emontenegr/spidey/rrc v0.0.0
)

replace github.com/emontenegr/spidey/gen/go => `+filepath.ToSlash(genGoPath)+`
replace github.com/emontenegr/spidey/rrc => `+filepath.ToSlash(rrcPath)+`
`), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import (
	"context"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
)

type stubScorer struct{}

func (stubScorer) Score(_ context.Context, _ string, candidates []string) ([]float64, error) {
	out := make([]float64, len(candidates))
	for i := range out {
		out[i] = 0.5
	}
	return out, nil
}

func main() {
	cfg := rrc.DefaultConfig()
	e := rrc.NewEngine(cfg, stubScorer{})
	_ = e.Config()
	_ = (*pb.Message)(nil)
}
`), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	// `go mod tidy` populates go.sum for the foreign module's
	// transitive deps (protobuf, etc). Replace directives keep
	// gen/go and rrc local; everything else resolves through the
	// module proxy. Cache the existing GOPATH cache (already warm
	// from the workspace build) so this is offline-friendly.
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	tidy.Env = append(os.Environ(), "GOWORK=off")
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("foreign module tidy failed: %v\n%s", err, out)
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "smoke.exe"), ".")
	cmd.Dir = dir
	// GOWORK=off ensures the foreign module resolves only via its
	// own go.mod replace directives, not the parent workspace.
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("foreign module build failed: %v\n%s", err, out)
	}
}

// findRepoRoot walks up from cwd until it finds a directory
// containing both go.work and an rrc/ child — the spidey repo
// root. Tests run from anywhere inside the repo without hardcoded
// paths.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if hasFile(dir, "go.work") && hasDir(dir, "rrc") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", &repoRootErr{cwd: mustCwd()}
		}
		dir = parent
	}
}

func hasFile(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err == nil && !info.IsDir()
}

func hasDir(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err == nil && info.IsDir()
}

func mustCwd() string {
	d, _ := os.Getwd()
	return d
}

type repoRootErr struct{ cwd string }

func (e *repoRootErr) Error() string {
	return "repo root not found from " + e.cwd + " (no ancestor has both go.work and rrc/)"
}

// avoid unused-import lint when extending later.
var _ = strings.Contains
