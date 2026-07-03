package calibrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// persisted is the on-disk form of a fitted calibrator plus the metadata
// needed to know whether it still applies. ScorerModelID records which
// reranker's score distribution the coefficients were fit against — a
// calibrator is only valid for the model it was trained on, so the loader
// can refuse a stale one after a scorer swap.
type persisted struct {
	Calibrator    Calibrator `json:"calibrator"`
	ScorerModelID string     `json:"scorer_model_id"`
	Samples       int        `json:"samples"`
	LogLoss       float64    `json:"log_loss"`
}

// Save writes a fitted calibrator to path as JSON, creating parent dirs.
func Save(path string, c Calibrator, scorerModelID string, samples int, logLoss float64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("calibrate.Save: mkdir: %w", err)
	}
	b, err := json.MarshalIndent(persisted{
		Calibrator:    c,
		ScorerModelID: scorerModelID,
		Samples:       samples,
		LogLoss:       logLoss,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("calibrate.Save: marshal: %w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("calibrate.Save: write: %w", err)
	}
	return nil
}

// Load reads a fitted calibrator from path. Returns (calibrator, true, nil)
// when a valid calibrator for scorerModelID exists; (_, false, nil) when the
// file is absent or was fit against a different scorer (caller falls back to
// the bootstrap); (_, false, err) only on a genuinely corrupt file. This
// tri-state keeps "no calibrator yet" a normal boot path, not an error.
func Load(path, scorerModelID string) (Calibrator, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Calibrator{}, false, nil
	}
	if err != nil {
		return Calibrator{}, false, fmt.Errorf("calibrate.Load: read: %w", err)
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		return Calibrator{}, false, fmt.Errorf("calibrate.Load: parse %s: %w", path, err)
	}
	// A calibrator fit against a different reranker's score distribution
	// does not apply — refuse it rather than silently mis-gate.
	if scorerModelID != "" && p.ScorerModelID != "" && p.ScorerModelID != scorerModelID {
		return Calibrator{}, false, nil
	}
	return p.Calibrator, true, nil
}
