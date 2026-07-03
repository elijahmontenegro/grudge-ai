package calibrate

import (
	"context"
	"fmt"
)

// CandidateSignals is one (Local Context, candidate) pair to label: the raw
// signals plus the identifiers the judge needs to regenerate the turn with
// the candidate force-included.
type CandidateSignals struct {
	TurnID      string
	CandidateID string
	Sim         float64
	Mass        float64
	// WasIncluded reports whether the live policy actually selected this
	// candidate for the turn. The harness deliberately over-samples the
	// EXCLUDED ones — the coverage-bias fix: you can only learn the value
	// of an action the policy didn't take by sometimes taking it.
	WasIncluded bool
}

// CounterfactualJudge decides whether force-including a candidate makes a
// turn's regenerated continuation better — the ground-truth signal. This is
// the seam where the live implementation plugs in: it force-includes the
// candidate, regenerates the continuation with it present, and compares
// against the without-it continuation (LLM-judge for open conversation, task
// metric where task-shaped). Comparing two FRESH regenerations is what avoids
// the routing-around bias of scoring a historical target born without the
// candidate. Tests inject a deterministic fake.
//
// The judge routes through what the model does with vs. without the
// candidate — never through whether selection picked it — so the label is
// independent of the signals being calibrated. That independence is the whole
// point; do not implement a judge that reads Sim/Mass.
type CounterfactualJudge interface {
	// IsPrerequisite reports whether the candidate is a true prerequisite of
	// the turn, judged by regenerative counterfactual coherence.
	IsPrerequisite(ctx context.Context, turnID, candidateID string) (bool, error)
}

// LabelConfig controls the labeling sweep. The exploration mix (randomized
// forced inclusion of excluded candidates) is the caller's responsibility —
// it assembles `candidates` with the right include/exclude balance, so the
// harness itself stays deterministic and rand-free for reproducible replays.
type LabelConfig struct {
	// MaxSamples caps total labels produced (0 = no cap). A visible bound —
	// the harness logs when it truncates so a partial sweep is never
	// mistaken for full coverage.
	MaxSamples int
}

// LabelResult is the output of a labeling sweep: the training set for Fit,
// plus coverage accounting so a bounded sweep is legible.
type LabelResult struct {
	Samples         []LabeledSample
	Considered      int
	IncludedLabeled int
	ExcludedLabeled int // the coverage-bias-breaking samples
	Truncated       bool
}

// Label runs the offline labeling sweep: for each candidate, ask the
// counterfactual judge whether it was a true prerequisite, and emit a
// LabeledSample pairing that verdict with the raw signals. Because it only
// needs the judge and the pre-computed signals, the whole sweep is offline
// replay over the lossless corpus — it never touches a live user, only
// compute. Deterministic given the same inputs and judge.
//
// The caller is responsible for having assembled `candidates` with the right
// exploration mix (over-sampling excluded candidates via randomized forced
// inclusion); Label itself just drives the judge and pairs verdicts with
// signals, so it stays pure and testable.
func Label(ctx context.Context, judge CounterfactualJudge, candidates []CandidateSignals, cfg LabelConfig) (LabelResult, error) {
	if judge == nil {
		return LabelResult{}, fmt.Errorf("calibrate.Label: nil judge")
	}
	var res LabelResult
	for _, cand := range candidates {
		if cfg.MaxSamples > 0 && len(res.Samples) >= cfg.MaxSamples {
			res.Truncated = true
			break
		}
		res.Considered++
		isPrereq, err := judge.IsPrerequisite(ctx, cand.TurnID, cand.CandidateID)
		if err != nil {
			return res, fmt.Errorf("judge turn=%s candidate=%s: %w", cand.TurnID, cand.CandidateID, err)
		}
		res.Samples = append(res.Samples, LabeledSample{
			Sim:      cand.Sim,
			Mass:     cand.Mass,
			IsPrereq: isPrereq,
		})
		if cand.WasIncluded {
			res.IncludedLabeled++
		} else {
			res.ExcludedLabeled++
		}
	}
	return res, nil
}

// FitFromLabels is the end-to-end convenience: run the sweep, then fit. The
// two are separable (Label / Fit) so a caller can inspect the training set,
// seed it with an offline human/dataset set for cold-start, or persist it.
func FitFromLabels(ctx context.Context, judge CounterfactualJudge, candidates []CandidateSignals, lc LabelConfig, fc FitConfig) (*Calibrator, LabelResult, error) {
	res, err := Label(ctx, judge, candidates, lc)
	if err != nil {
		return nil, res, err
	}
	cal, err := Fit(res.Samples, fc)
	if err != nil {
		return nil, res, err
	}
	return cal, res, nil
}
