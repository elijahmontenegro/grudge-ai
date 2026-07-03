package calibrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
)

// Artifact is the on-disk form of a fitted calibrator plus the metadata
// needed to know whether it still applies and when to refine it.
// ScorerModelID records which reranker's score distribution the
// coefficients were fit against — a calibrator is only valid for the
// model it was trained on, so the loader can refuse a stale one after a
// scorer swap. MassSamples/ProvenanceEdgesAtFit are the mass-refit
// watermark: zero MassSamples means B still carries the seed fit's
// ratio prior; the refit re-arms when the corpus's provenance structure
// has doubled past ProvenanceEdgesAtFit.
type Artifact struct {
	Calibrator    Calibrator `json:"calibrator"`
	ScorerModelID string     `json:"scorer_model_id"`
	Samples       int        `json:"samples"`
	LogLoss       float64    `json:"log_loss"`

	MassSamples          int `json:"mass_samples,omitempty"`
	ProvenanceEdgesAtFit int `json:"provenance_edges_at_fit,omitempty"`
	// MassAttemptEdges is failure memory: the provenance-edge count at
	// the last FAILED mass-refit attempt. Re-arming waits for the corpus
	// to double past it, so a structurally doomed or judge-broken replay
	// retries on growth, not on every reload.
	MassAttemptEdges int `json:"mass_attempt_edges,omitempty"`
}

// Save writes a fitted calibrator artifact to path as JSON, creating
// parent dirs. The composed load-or-fit-and-save lifecycle lives with
// the fit source (seedfit.EnsureFitted) — deliberately: this package
// stays pure math + artifact IO; the corpus-replay mass refit is a
// different lifecycle (watermark-gated refinement), not a load-or-fit.
func Save(path string, a Artifact) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("calibrate.Save: mkdir: %w", err)
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("calibrate.Save: marshal: %w", err)
	}
	// Atomic: write a sibling temp file and rename. A crash or ENOSPC
	// mid-write must not leave torn JSON at the canonical path — a torn
	// artifact would fail every subsequent Load and wedge calibration.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("calibrate.Save: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("calibrate.Save: rename: %w", err)
	}
	return nil
}

// Load reads a fitted calibrator artifact from path. Returns
// (artifact, true, nil) when a valid artifact for scorerModelID exists;
// (_, false, nil) when the file is absent or was fit against a
// different scorer (caller falls back to the bootstrap); (_, false,
// err) only on a genuinely corrupt file. This tri-state keeps "no
// calibrator yet" a normal boot path, not an error.
func Load(path, scorerModelID string) (Artifact, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Artifact{}, false, nil
	}
	if err != nil {
		return Artifact{}, false, fmt.Errorf("calibrate.Load: read: %w", err)
	}
	var a Artifact
	if err := json.Unmarshal(b, &a); err != nil {
		return Artifact{}, false, fmt.Errorf("calibrate.Load: parse %s: %w", path, err)
	}
	// A calibrator fit against a different reranker's score distribution
	// does not apply — refuse it rather than silently mis-gate. Strict:
	// an artifact with no recorded scorer id is refused for any scorer
	// (an empty id must not act as a wildcard).
	if a.ScorerModelID == "" || (scorerModelID != "" && a.ScorerModelID != scorerModelID) {
		return Artifact{}, false, nil
	}
	// Coefficient sanity: a parseable-but-degenerate artifact (hand
	// edit, partial legacy write) must not become the live calibrator.
	if !(a.Calibrator.A > 0) || math.IsNaN(a.Calibrator.B) || math.IsInf(a.Calibrator.B, 0) ||
		math.IsNaN(a.Calibrator.C) || math.IsInf(a.Calibrator.C, 0) || math.IsInf(a.Calibrator.A, 0) {
		return Artifact{}, false, fmt.Errorf("calibrate.Load: degenerate coefficients in %s (%+v)", path, a.Calibrator)
	}
	return a, true, nil
}
