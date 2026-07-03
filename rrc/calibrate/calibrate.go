// Package calibrate turns raw prerequisite signals into a calibrated
// probability that a candidate is a true prerequisite. It is the model A4's
// acceptance mechanism consumes: instead of a hand-tuned EdgeThreshold on a
// raw cross-encoder score, both the semantic-similarity signal and the
// structural descendant-mass signal are mapped into the same currency —
// P(prereq | signals) — and fused. Because both terms speak in probability,
// they combine without a free weight λ; the fusion coefficients are LEARNED
// by maximum likelihood against ground-truth labels, not chosen by taste.
//
// The ground truth those labels come from is regenerative counterfactual
// coherence (see package harness): does force-including a candidate and
// regenerating the turn actually produce a better continuation. That routes
// through what the model does, never through whether selection picked the
// candidate — breaking the circularity a self-referential label would have.
//
// This package is pure math + fitting; it has no LLM or storage dependency,
// so it is unit-testable in isolation and reused identically across scorer
// backends (per-model calibration is exactly what makes swapping zerank for
// a hosted ranker safe — the coefficients re-fit, the mechanism does not).
package calibrate

import (
	"errors"
	"fmt"
	"math"
)

// LabeledSample is one training example: the two raw signals for a candidate
// and whether it was a true prerequisite (from the counterfactual judge).
type LabeledSample struct {
	Sim      float64 // semantic similarity signal (e.g. cross-encoder score)
	Mass     float64 // structural descendant-mass signal (provenance)
	IsPrereq bool    // ground-truth label
}

// Calibrator is the fitted joint model P(prereq | sim, mass) =
// σ(a·sim + b·mass + c). The coefficients are learned; `b/a` is the
// data-derived tradeoff between structural and semantic evidence — the
// honest, non-taste form of what a hand-tuned λ tried to be.
type Calibrator struct {
	A float64 `json:"a"` // similarity coefficient
	B float64 `json:"b"` // mass coefficient
	C float64 `json:"c"` // bias
}

// Predict returns P(prereq | sim, mass) in (0,1). This is the same-currency
// term A4 fuses with the budget shadow price: accept when expected value
// (P·gain − (1−P)·harm) clears the marginal token price.
func (c Calibrator) Predict(sim, mass float64) float64 {
	return sigmoid(c.A*sim + c.B*mass + c.C)
}

func sigmoid(z float64) float64 {
	// Numerically stable across both signs of z.
	if z >= 0 {
		return 1 / (1 + math.Exp(-z))
	}
	ez := math.Exp(z)
	return ez / (1 + ez)
}

// Bootstrap returns a calibrator that reproduces a flat similarity-threshold
// operating point: Predict(threshold, 0) == 0.5, steep in similarity, with a
// positive mass coefficient so structural signal can lift a low-similarity
// candidate over the boundary (the /\). It is the pre-fit default used until
// the offline pipeline learns real coefficients; `steep` controls the slope
// (higher = closer to a hard step at the threshold), `massCoef` the structural
// lift. This is a bootstrap, not a tuned magic number — the empirical flip is
// a fitted Calibrator.
func Bootstrap(threshold, steep, massCoef float64) Calibrator {
	return Calibrator{A: steep, B: massCoef, C: -steep * threshold}
}

// FitConfig controls the MLE fit. Zero value gives sensible defaults via Fit.
type FitConfig struct {
	LearningRate float64
	Iterations   int
	L2           float64 // ridge penalty; keeps coefficients finite on separable data
}

func (f FitConfig) withDefaults() FitConfig {
	if f.LearningRate <= 0 {
		f.LearningRate = 0.1
	}
	if f.Iterations <= 0 {
		f.Iterations = 5000
	}
	if f.L2 < 0 {
		f.L2 = 0
	}
	return f
}

// Fit learns the joint logistic model by gradient descent on regularized
// log-loss. Deterministic (no randomness — full-batch gradient), so the same
// samples always yield the same coefficients; that determinism matters
// because the whole point is a re-fit-on-drift model, not a hand-set number.
// Returns an error only on empty input.
func Fit(samples []LabeledSample, cfg FitConfig) (*Calibrator, error) {
	if len(samples) == 0 {
		return nil, fmt.Errorf("calibrate.Fit: no samples")
	}
	cfg = cfg.withDefaults()
	c := &Calibrator{}
	n := float64(len(samples))

	for iter := 0; iter < cfg.Iterations; iter++ {
		var gA, gB, gC float64
		for _, s := range samples {
			p := c.Predict(s.Sim, s.Mass)
			y := 0.0
			if s.IsPrereq {
				y = 1.0
			}
			err := p - y // dLoss/dz for log-loss + sigmoid
			gA += err * s.Sim
			gB += err * s.Mass
			gC += err
		}
		// Mean gradient + L2 (not applied to bias).
		gA = gA/n + cfg.L2*c.A
		gB = gB/n + cfg.L2*c.B
		gC = gC / n
		c.A -= cfg.LearningRate * gA
		c.B -= cfg.LearningRate * gB
		c.C -= cfg.LearningRate * gC
	}
	return c, nil
}

// LogLoss returns the mean negative log-likelihood of the calibrator on the
// samples — the fit-quality metric. Lower is better; used by tests and by
// the harness to report convergence.
func (c Calibrator) LogLoss(samples []LabeledSample) float64 {
	if len(samples) == 0 {
		return 0
	}
	const eps = 1e-12
	var sum float64
	for _, s := range samples {
		p := c.Predict(s.Sim, s.Mass)
		p = math.Min(math.Max(p, eps), 1-eps)
		if s.IsPrereq {
			sum += -math.Log(p)
		} else {
			sum += -math.Log(1 - p)
		}
	}
	return sum / float64(len(samples))
}

// PriorLogLoss is the log-loss of the best label-prior-only predictor
// over samples — the constant prediction p = positives/total. It is
// the no-skill baseline: a fitted calibrator whose LogLoss does not
// beat this is not using the score signal at all (it has collapsed to
// predicting the base rate). Exposed so every fit path — seed fit
// today, corpus-replay mass fit later, live health checks — measures
// validity against the same baseline.
func PriorLogLoss(samples []LabeledSample) float64 {
	if len(samples) == 0 {
		return 0
	}
	var pos int
	for _, s := range samples {
		if s.IsPrereq {
			pos++
		}
	}
	p := float64(pos) / float64(len(samples))
	if p <= 0 || p >= 1 {
		// Single-class data: the prior predicts it perfectly.
		return 0
	}
	return -(p*math.Log(p) + (1-p)*math.Log(1-p))
}

// minFitSkill is the validity floor for any calibrator judged against
// labeled samples, measured as skill over the label-prior baseline:
// skill = 1 − LogLoss/PriorLogLoss. A scorer that separates labeled
// positives from negatives at all clears it easily (zerank's first
// live seed fit: log-loss 0.330 vs prior 0.500 → skill ≈ 0.34); a
// collapsed scorer yields a flat base-rate calibrator with skill ≈ 0.
// Deliberately loose — the refused failure is catastrophic
// non-discrimination, not subtle mis-calibration.
const minFitSkill = 0.10

// ErrInvalid marks a calibrator/scorer pairing that failed the
// absolute validity predicate. Callers branch on it (errors.Is) to
// distinguish "the pairing is broken — refit" from transport errors
// ("could not check — skip").
var ErrInvalid = errors.New("calibrator/scorer pairing invalid")

// Validate is the absolute validity predicate for a calibrator against
// labeled samples: the similarity association must be positive (A > 0 —
// an anti-correlated or score-blind calibrator is a broken pairing,
// whatever its log-loss) and the calibrator must show real skill over
// the no-signal prior baseline. Absolute against the labels, never
// relative to a prior fit: a flat calibrator scores the same log-loss
// on healthy and garbage input, so relative comparisons cannot detect
// their own poisoning. Shared by every consumer that must refuse an
// invalid pairing — the seed fit before persisting, the boot-time
// scorer health check, and the corpus-replay mass refit.
func Validate(c Calibrator, samples []LabeledSample) error {
	if c.A <= 0 {
		return fmt.Errorf("calibrate: similarity coefficient A=%.3f ≤ 0 — scores are uncorrelated or anti-correlated with the labels (scorer collapse or wrong model at endpoint?): %w", c.A, ErrInvalid)
	}
	logLoss := c.LogLoss(samples)
	priorLL := PriorLogLoss(samples)
	skill := 0.0
	if priorLL > 0 {
		skill = 1 - logLoss/priorLL
	}
	if skill < minFitSkill {
		return fmt.Errorf("calibrate: no discrimination on the labeled samples (log-loss %.4f vs prior baseline %.4f, skill %.2f < %.2f) — scorer collapse or wrong model at endpoint?: %w",
			logLoss, priorLL, skill, minFitSkill, ErrInvalid)
	}
	return nil
}
