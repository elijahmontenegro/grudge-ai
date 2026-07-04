// Package tokenscale grounds the engine's pre-send token counter in
// provider-reported usage. Providers report token usage at request
// granularity — one prompt_tokens total for the entire serialized
// window — and that number is ground truth from the model's own
// tokenizer. The counter the engine sums with (chunk.Config.Estimator)
// is a lookalike; its *relative* counts are useful but its absolute
// scale drifts per model. This package learns, per model key, the
// ratio between what the counter predicted for a sent wire and what
// the provider reported for it, so the caller can convert a
// model-truth budget into counter units before assembly.
//
// The learner is a windowed max over admitted ratios. The observation
// channel is censored from below only: a KV-prefix cache hit or a
// serving-side truncation can make the reported prompt count smaller
// than the true prompt, never larger. A max ignores censored lowballs
// whenever any approximately-cold call lands in the window (at least
// the first call of a turn, whose assembled wire differs from the
// previous turn's), the window bounds the lifetime of a one-off
// over-report anomaly, and a stale max ages out naturally when a
// codec or template change shifts the true ratio. There is no decay
// path for censored observations to poison.
//
// Admission is gated, with internal constants rather than knobs:
// observations from wires smaller than a quarter of the budget are
// skipped (the predictor is affine — per-message and fixed terms
// dominate small wires and would bias a max learner high; wires near
// the budget are also the only regime where grounding matters), and
// ratios outside [0.5, 3.0] are rejected with an error for the
// caller to log — a real tokenizer+template divergence lives inside
// that band, so an outlier is diagnostic of censoring or a provider
// usage bug, not of a tokenizer.
//
// State persists as one JSON artifact (atomic tmp+rename, same
// doctrine as rrc/calibrate): a corrupt file is a refittable cache —
// Open returns a usable empty store alongside the parse error so the
// caller can log it loudly and carry on; the next save overwrites.
package tokenscale

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sync"
)

const (
	// window is the ring size per model: how many admitted ratios the
	// max is computed over. Large enough that cold calls land in it,
	// small enough that a stale high-water mark ages out.
	window = 32
	// sizeGateDivisor: admit only observations whose prediction is at
	// least budget/sizeGateDivisor.
	sizeGateDivisor = 4
	// ratioMin/ratioMax bound plausible counter-vs-model divergence.
	ratioMin = 0.5
	ratioMax = 3.0
)

// ErrRatioOutOfBand marks an observation whose observed/predicted
// ratio falls outside the plausible band — diagnostic of a provider
// usage bug or extreme censoring, never admitted into the ring.
var ErrRatioOutOfBand = errors.New("tokenscale: ratio outside plausible band")

// modelState is one model's ring of admitted ratios. Ratios is
// ordered oldest→newest and capped at window entries; Samples counts
// all admissions ever (diagnostic only).
type modelState struct {
	Ratios  []float64 `json:"ratios"`
	Samples int       `json:"samples"`
}

type fileState struct {
	Models map[string]*modelState `json:"models"`
}

// Store is the process-wide token-scale state: one JSON artifact
// holding a ratio ring per model key. All access is serialized by one
// mutex; the mutex is a leaf — nothing else is ever locked while it
// is held, and Observe performs only in-memory updates plus one small
// file write under it.
type Store struct {
	mu     sync.Mutex
	path   string
	models map[string]*modelState
}

// Open loads the store at path. An absent file is a normal cold
// start. A corrupt or degenerate file is a refittable cache: Open
// returns a USABLE empty store together with the parse error — the
// caller logs it and proceeds; the next admitted observation
// overwrites the corrupt artifact. Only a genuine read failure
// (permissions, IO) returns a nil store.
func Open(path string) (*Store, error) {
	s := &Store{path: path, models: make(map[string]*modelState)}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("tokenscale.Open: read: %w", err)
	}
	var state fileState
	if err := json.Unmarshal(b, &state); err != nil {
		return s, fmt.Errorf("tokenscale.Open: corrupt artifact %s (starting empty, will overwrite): %w", path, err)
	}
	for key, m := range state.Models {
		if key == "" || m == nil || !validRatios(m.Ratios) {
			return &Store{path: path, models: make(map[string]*modelState)},
				fmt.Errorf("tokenscale.Open: degenerate entries in %s (starting empty, will overwrite)", path)
		}
	}
	if state.Models != nil {
		s.models = state.Models
	}
	return s, nil
}

// validRatios rejects rings a hand edit or torn legacy write could
// have left behind: non-finite values, out-of-band ratios, oversize
// rings.
func validRatios(ratios []float64) bool {
	if len(ratios) > window {
		return false
	}
	for _, r := range ratios {
		if math.IsNaN(r) || math.IsInf(r, 0) || r < ratioMin || r > ratioMax {
			return false
		}
	}
	return true
}

// Bound returns a handle pre-bound to one model key, so consumers
// carry no key plumbing. Returns nil for an empty key — the caller
// composing handles decides what "no model configured" means; a nil
// *Bound must not be wrapped in a non-nil interface.
func (s *Store) Bound(key string) *Bound {
	if key == "" {
		return nil
	}
	return &Bound{store: s, key: key}
}

// Bound is a Store handle fixed to one model key.
type Bound struct {
	store *Store
	key   string
}

// Scale returns the learned counter→model ratio for the bound model:
// the max over the ring of admitted ratios, or 0 when nothing has
// been admitted yet (caller treats 0 as "ungrounded — use the budget
// unconverted").
func (b *Bound) Scale() float64 {
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	m := b.store.models[b.key]
	if m == nil || len(m.Ratios) == 0 {
		return 0
	}
	max := 0.0
	for _, r := range m.Ratios {
		if r > max {
			max = r
		}
	}
	return max
}

// Observe feeds one (predicted, observed) pair from a successfully
// sent request. predicted is the counter-unit total the assembler
// computed for the sent wire; observed is the provider-reported
// prompt_tokens; budget is the counter-unit budget the assembly ran
// under (the size gate's baseline). Non-positive inputs and
// below-gate wires are skipped silently — they are not observations.
// An out-of-band ratio returns ErrRatioOutOfBand (wrapped, with
// values) for the caller to log; a persistence failure returns the
// write error. In both cases the turn that produced the observation
// has already succeeded — callers log loudly and move on.
func (b *Bound) Observe(predicted, observed, budget int) error {
	if predicted <= 0 || observed <= 0 || budget <= 0 {
		return nil
	}
	if predicted*sizeGateDivisor < budget {
		return nil
	}
	ratio := float64(observed) / float64(predicted)
	if ratio < ratioMin || ratio > ratioMax {
		return fmt.Errorf("%w: %s observed/predicted = %d/%d = %.3f (band [%.1f, %.1f])",
			ErrRatioOutOfBand, b.key, observed, predicted, ratio, ratioMin, ratioMax)
	}

	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	m := b.store.models[b.key]
	if m == nil {
		m = &modelState{}
		b.store.models[b.key] = m
	}
	m.Ratios = append(m.Ratios, ratio)
	if len(m.Ratios) > window {
		m.Ratios = m.Ratios[len(m.Ratios)-window:]
	}
	m.Samples++
	return b.store.saveLocked()
}

// saveLocked writes the artifact atomically (sibling temp + rename).
// Caller holds s.mu.
func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("tokenscale: mkdir: %w", err)
	}
	b, err := json.MarshalIndent(fileState{Models: s.models}, "", "  ")
	if err != nil {
		return fmt.Errorf("tokenscale: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("tokenscale: write: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("tokenscale: rename: %w", err)
	}
	return nil
}
