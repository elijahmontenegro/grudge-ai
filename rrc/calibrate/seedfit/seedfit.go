// Package seedfit fits an acceptance calibrator from the labeled seed set:
// it scores each (query, candidate) pair with a real scorer, pairs those raw
// similarity scores with the seed labels (correct = prerequisite, distractor
// = not), and runs the MLE fit. It is the one implementation of seed-set
// calibration, shared by the runtime's background auto-calibration
// (substrate.Holder) and the cmd/calibrate dev tool — so there is exactly one
// definition of "how a scorer gets calibrated."
//
// Like regenjudge, it lives in a subpackage because it needs core.Scorer;
// the calibrate package proper stays pure math.
package seedfit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/rrc/calibrate"
)

// triple is one labeled seed entry: a query, its true prerequisite, and
// distractors that merely resemble it.
type triple struct {
	ID          string   `json:"id"`
	Query       string   `json:"query"`
	Correct     string   `json:"correct"`
	Distractors []string `json:"distractors"`
}

type pairsFile struct {
	Categories map[string][]triple `json:"categories"`
}

// Result reports what the fit was trained on, for logs and the persisted
// artifact's metadata.
type Result struct {
	Calibrator calibrate.Calibrator
	Samples    int
	Positives  int
	Negatives  int
	LogLoss    float64
}

// Fit scores the seed set with scorer and fits the calibrator. seedJSON is
// the pairs file (eval.Seed() for the embedded copy, or a file read by the
// dev tool). Mass is 0 for every seed sample — a static eval set carries no
// provenance signal — so the seed can only inform the similarity axis (A, C).
//
// The mass coefficient (B) is therefore NOT taken from this fit. Zero-mass
// data gives B an identically-zero gradient: the MLE would return B=0, and
// swapping that in would silently disable the structural lift — a
// provenance-connected root at near-zero similarity would stop clearing
// acceptance, undoing the whole /\ design the moment auto-calibration ran.
// An uninformative dataset must carry the prior forward, not zero it.
//
// The prior is carried as a RATIO, not a raw coefficient, because B's
// meaning depends on A's scale and the fit changes A. In the bootstrap,
// B/A = 0.5 means "full structural mass shifts the acceptance boundary left
// by 0.5 similarity points" (0.60 → 0.10 at mass=1) — that boundary-shift
// is the /\'s design semantic. Preserving raw B against a refit A would
// silently rescale that shift; preserving B/A keeps it. So the fitted
// calibrator gets B = (prior.B/prior.A) · fittedA. B's true empirical value
// comes later, from provenance-mass labeling over a replayed corpus
// (rrc/calibrate/regenjudge), where mass actually varies in the data.
//
// Scoring failures on individual triples abort the fit rather than silently
// thinning the training set: a half-scored seed produces a calibrator that
// looks fitted but wasn't, which is worse than falling back to the bootstrap.
func Fit(ctx context.Context, scorer core.Scorer, seedJSON []byte, prior calibrate.Calibrator) (Result, error) {
	if scorer == nil {
		return Result{}, fmt.Errorf("seedfit: nil scorer")
	}
	var p pairsFile
	if err := json.Unmarshal(seedJSON, &p); err != nil {
		return Result{}, fmt.Errorf("seedfit: parse seed set: %w", err)
	}

	var samples []calibrate.LabeledSample
	var pos, neg int
	for cat, items := range p.Categories {
		for _, t := range items {
			candidates := append([]string{t.Correct}, t.Distractors...)
			scores, err := scorer.Score(ctx, t.Query, candidates)
			if err != nil {
				return Result{}, fmt.Errorf("seedfit: score %s/%s: %w", cat, t.ID, err)
			}
			if len(scores) != len(candidates) {
				return Result{}, fmt.Errorf("seedfit: %s/%s: %d scores for %d candidates", cat, t.ID, len(scores), len(candidates))
			}
			for i, s := range scores {
				isPrereq := i == 0 // index 0 is the correct (prerequisite) candidate
				samples = append(samples, calibrate.LabeledSample{Sim: s, Mass: 0, IsPrereq: isPrereq})
				if isPrereq {
					pos++
				} else {
					neg++
				}
			}
		}
	}
	if len(samples) == 0 {
		return Result{}, fmt.Errorf("seedfit: seed set produced no samples")
	}

	cal, err := calibrate.Fit(samples, calibrate.FitConfig{L2: 1e-4})
	if err != nil {
		return Result{}, fmt.Errorf("seedfit: fit: %w", err)
	}
	// Preserve the structural lift: the seed carried no mass signal, so the
	// fitted B is a meaningless 0. Carry the prior's boundary-shift ratio
	// B/A onto the fitted similarity scale (see doc comment).
	if prior.A != 0 {
		cal.B = (prior.B / prior.A) * cal.A
	}
	return Result{
		Calibrator: *cal,
		Samples:    len(samples),
		Positives:  pos,
		Negatives:  neg,
		LogLoss:    cal.LogLoss(samples),
	}, nil
}
