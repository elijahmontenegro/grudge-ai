// Package seedfit fits an acceptance calibrator from the labeled seed set:
// it scores each (query, candidate) pair with a real scorer, pairs those raw
// similarity scores with the seed labels (correct = prerequisite, distractor
// = not), and runs the MLE fit. It is the one implementation of seed-set
// calibration, consumed by the runtime's background auto-calibration
// (substrate.Holder) — so there is exactly one definition of "how a
// scorer gets calibrated."
//
// It lives in a subpackage because it needs a live scorer; the calibrate
// package proper stays pure math. The Scorer contract is declared locally
// (one method) so the rrc stratum never imports core — any core.Scorer
// satisfies it structurally.
package seedfit

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"sort"

	"github.com/elijahmontenegro/grudge/rrc/calibrate"
)

// Scorer is the one-method scoring contract the fit needs. Structurally
// identical to rrc.Scorer / core.Scorer, declared here so importing the
// fit pulls neither.
type Scorer interface {
	Score(ctx context.Context, query string, candidates []string) ([]float64, error)
}

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
// the pairs file (seed.Pairs() for the embedded copy). Mass is 0 for every
// seed sample — a static eval set carries no provenance signal — so the
// seed can only inform the similarity axis (A, C).
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
// — no such stage exists; B is a declared structural constant.
//
// Scoring failures on individual triples abort the fit rather than silently
// thinning the training set: a half-scored seed produces a calibrator that
// looks fitted but wasn't, which is worse than falling back to the bootstrap.
//
// The fit is also VALIDITY-GATED before it can be returned (and therefore
// before anything can persist it): the seed is labeled ground truth, so a
// working scorer+fit must separate the positives from the negatives better
// than the label prior alone. A scorer that collapsed to uniform output
// (template drift zeroing every pair, a wrong model at the endpoint) fits a
// flat base-rate calibrator with ~zero skill — persisting that would
// silently blind retrieval under a calibrator that ignores its input. Such
// a fit is refused with an error; the caller stays on its current
// calibrator and retries later. Validity is measured absolutely against
// the labels, never relative to a prior fit — a flat calibrator scores the
// same log-loss on healthy and garbage input, so relative comparisons
// cannot detect their own poisoning.
func Fit(ctx context.Context, scorer Scorer, seedJSON []byte, prior calibrate.Calibrator) (Result, error) {
	if scorer == nil {
		return Result{}, fmt.Errorf("seedfit: nil scorer")
	}
	samples, pos, neg, err := Samples(ctx, scorer, seedJSON, 0)
	if err != nil {
		return Result{}, err
	}

	cal, err := calibrate.Fit(samples, calibrate.FitConfig{L2: 1e-4})
	if err != nil {
		return Result{}, fmt.Errorf("seedfit: fit: %w", err)
	}

	// Validity gate (see doc comment): the fit must be refused before it
	// can be returned, and therefore before anything can persist it.
	if verr := calibrate.Validate(*cal, samples); verr != nil {
		return Result{}, fmt.Errorf("seedfit: refusing the fit: %w", verr)
	}
	// Persist-consistency gate: the boot health check will judge this
	// same calibrator on the deterministic seed SUBSAMPLE. A fit that
	// passes the full-set predicate but fails the subsample would
	// persist, then fail health on every boot — an oscillation of full
	// refits (and re-armed mass replays) that never converges. Gate
	// persistence on the exact predicate that judges it later.
	subSamples, _, _, err := Samples(ctx, scorer, seedJSON, healthTriples)
	if err != nil {
		return Result{}, fmt.Errorf("seedfit: subsample gate: %w", err)
	}
	if verr := calibrate.Validate(*cal, subSamples); verr != nil {
		return Result{}, fmt.Errorf("seedfit: refusing the fit (passes full seed but fails the boot-health subsample — would oscillate): %w", verr)
	}
	logLoss := cal.LogLoss(samples)

	// Preserve the structural lift: the seed carried no mass signal, so the
	// fitted B is a meaningless 0. Carry the prior's boundary-shift ratio
	// B/A onto the fitted similarity scale (see doc comment). A degenerate
	// prior (zero/non-finite A, or a non-positive lift ratio — e.g. a
	// hand-edited or corrupt artifact that slipped in as the live
	// calibrator) must not zero the /\; fall back to the canonical
	// bootstrap ratio (rrc.DefaultConfig's Bootstrap(0.60, 12, 6) → 0.5).
	ratio := bootstrapLiftRatio
	if prior.A > 0 && !math.IsNaN(prior.B/prior.A) && prior.B/prior.A > 0 {
		ratio = prior.B / prior.A
	}
	cal.B = ratio * cal.A
	return Result{
		Calibrator: *cal,
		Samples:    len(samples),
		Positives:  pos,
		Negatives:  neg,
		LogLoss:    logLoss,
	}, nil
}

// EnsureFitted is the load-or-fit-and-save composition: return the
// persisted calibrator for scorerModelID when one exists at path,
// otherwise fit from seedJSON (carrying prior's structural-lift ratio),
// persist the result, and return it. A non-nil Result reports a fresh
// fit (with its training stats); nil means the persisted artifact was
// used. This is the single definition of "make sure this scorer has a
// fitted calibrator" — callers own only trigger policy and lifecycle.
func EnsureFitted(ctx context.Context, scorer Scorer, scorerModelID, path string, seedJSON []byte, prior calibrate.Calibrator) (calibrate.Calibrator, *Result, error) {
	// A corrupt or degenerate artifact is a refittable cache, not user
	// data: fall through to fit-and-overwrite rather than wedging every
	// boot on the same broken file (substrate.Build already logged the
	// load failure loudly). Save is atomic, so the overwrite cannot
	// reproduce the torn state.
	if art, ok, err := calibrate.Load(path, scorerModelID); err == nil && ok {
		return art.Calibrator, nil, nil
	}
	res, err := Fit(ctx, scorer, seedJSON, prior)
	if err != nil {
		return calibrate.Calibrator{}, nil, err
	}
	if err := calibrate.Save(path, calibrate.Artifact{
		Calibrator:    res.Calibrator,
		ScorerModelID: scorerModelID,
		Samples:       res.Samples,
		LogLoss:       res.LogLoss,
	}); err != nil {
		return calibrate.Calibrator{}, nil, err
	}
	return res.Calibrator, &res, nil
}

// Samples scores the labeled seed set with scorer and returns
// calibration samples (mass=0 — the seed is static and carries no
// provenance signal). maxTriples > 0 bounds the work to a
// deterministic subsample: categories in sorted order, triples in file
// order — the same triples every call, so health checks compare like
// with like. maxTriples ≤ 0 scores everything.
func Samples(ctx context.Context, scorer Scorer, seedJSON []byte, maxTriples int) ([]calibrate.LabeledSample, int, int, error) {
	if scorer == nil {
		return nil, 0, 0, fmt.Errorf("seedfit: nil scorer")
	}
	var p pairsFile
	if err := json.Unmarshal(seedJSON, &p); err != nil {
		return nil, 0, 0, fmt.Errorf("seedfit: parse seed set: %w", err)
	}
	cats := make([]string, 0, len(p.Categories))
	for cat := range p.Categories {
		cats = append(cats, cat)
	}
	sort.Strings(cats)

	var samples []calibrate.LabeledSample
	var pos, neg int
	scoreTriple := func(cat string, t triple) error {
		// The positive sits at a query-derived index, not a fixed slot.
		// With the positive always first, a scorer keying on batch
		// position — or an adapter returning rank-sorted score arrays
		// (the classic /rerank index-remap bug) — separates the labels
		// perfectly while being blind to content. Rotation makes the
		// gate measure discrimination, not slot agreement. The index is
		// a pure function of the query so fakes and replays agree.
		rot := PositiveIndex(t.Query, len(t.Distractors)+1)
		candidates := make([]string, 0, len(t.Distractors)+1)
		candidates = append(candidates, t.Distractors[:rot]...)
		candidates = append(candidates, t.Correct)
		candidates = append(candidates, t.Distractors[rot:]...)
		scores, err := scorer.Score(ctx, t.Query, candidates)
		if err != nil {
			return fmt.Errorf("seedfit: score %s/%s: %w", cat, t.ID, err)
		}
		if len(scores) != len(candidates) {
			return fmt.Errorf("seedfit: %s/%s: %d scores for %d candidates", cat, t.ID, len(scores), len(candidates))
		}
		for i, s := range scores {
			if math.IsNaN(s) || math.IsInf(s, 0) {
				return fmt.Errorf("seedfit: non-finite score %v for %s/%s candidate %d: %w", s, cat, t.ID, i, calibrate.ErrInvalid)
			}
			isPrereq := i == rot
			samples = append(samples, calibrate.LabeledSample{Sim: s, Mass: 0, IsPrereq: isPrereq})
			if isPrereq {
				pos++
			} else {
				neg++
			}
		}
		return nil
	}

	if maxTriples > 0 {
		// Stratified subsample: round-robin one triple per category so
		// the health check covers every difficulty regime — including
		// the long-document triples where template drift shows first —
		// instead of whichever categories sort alphabetically.
		taken := 0
		for j := 0; taken < maxTriples; j++ {
			progressed := false
			for _, cat := range cats {
				if taken >= maxTriples {
					break
				}
				if j >= len(p.Categories[cat]) {
					continue
				}
				progressed = true
				if err := scoreTriple(cat, p.Categories[cat][j]); err != nil {
					return nil, 0, 0, err
				}
				taken++
			}
			if !progressed {
				break
			}
		}
	} else {
		for _, cat := range cats {
			for _, t := range p.Categories[cat] {
				if err := scoreTriple(cat, t); err != nil {
					return nil, 0, 0, err
				}
			}
		}
	}
	if len(samples) == 0 {
		return nil, 0, 0, fmt.Errorf("seedfit: seed set produced no samples")
	}
	return samples, pos, neg, nil
}

// PositiveIndex is the deterministic slot the correct candidate
// occupies in a triple's Score batch: a pure function of the query, so
// every consumer of the seed protocol (the fit, health checks, test
// fakes) derives the same rotation independently.
func PositiveIndex(query string, candidateCount int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(query))
	return int(h.Sum32() % uint32(candidateCount))
}

// bootstrapLiftRatio is the canonical structural-lift ratio B/A the
// bootstrap calibrator ships (calibrate.Bootstrap(0.60, 12, 6) → 6/12),
// used as the carry fallback when the prior is degenerate.
const bootstrapLiftRatio = 0.5

// healthTriples bounds the boot-time health check's scorer work: a
// deterministic seed subsample big enough to expose catastrophic
// non-discrimination, small enough to be a background blip (~8 scorer
// batch calls). The check refuses false confidence, not subtle drift.
//
// The subsample is STRATIFIED — one triple per category, round-robin —
// so health covers every difficulty regime, including the
// long-document triples where template drift manifests first. Churn
// safety does not depend on composition: Fit's persist-consistency
// gate guarantees any persisted calibrator already passes this exact
// subsample predicate, so a healthy pairing can never oscillate
// between passing the fit and failing boot health.
const healthTriples = 8

// Health verifies that the persisted calibrator still describes the
// LIVE scorer: it scores the deterministic seed subsample through the
// scorer and applies the absolute validity predicate
// (calibrate.Validate) to the pairing. nil means healthy. A transport
// error means "could not check" (callers skip — they don't refit); a
// validation error means the pairing is broken NOW — template drift,
// a swapped-but-same-id model, a scorer collapse — and the caller
// should refit (the fit's own validity gate makes a refit against a
// still-broken scorer unpersistable).
func Health(ctx context.Context, scorer Scorer, seedJSON []byte, cal calibrate.Calibrator) error {
	samples, _, _, err := Samples(ctx, scorer, seedJSON, healthTriples)
	if err != nil {
		return fmt.Errorf("seedfit health: %w", err)
	}
	return calibrate.Validate(cal, samples)
}
