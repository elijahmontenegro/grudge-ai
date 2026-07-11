package rrc

import (
	"hash/fnv"
	"math"
)

// Detection math for the acceptance law. Acceptance is DETECTION against
// a noise reference the event measures about itself: per selection
// event, a deterministic pseudo-random sample of the searchable corpus
// is scored against the same query with the same aggregation as the
// candidates, and a candidate is accepted when it stands out from that
// sample. Rank statistics make this exact and distribution-free — under
// the null "this candidate is corpus background", the probability that
// it outranks the whole reference sample is 1/(R+1) by exchangeability,
// whatever the scorer's score distribution looks like. No calibrator,
// no fitted curve, no labels: swap the scorer and the bar is correct at
// the next event, because the bar is re-measured at every event.
//
// Every statistic here is computed inside the selection event that
// consumes it and discarded — the perishable-inference law holds by
// construction, not by discipline.

// ReferenceSampleSize is R: how many reference chunks each selection
// event draws to measure its noise floor. Exported as the single source
// of truth for telemetry and the invariance bench. A resource constant
// (sample size for floor estimation — HOW-MUCH class, like
// provenanceReachCap): it sets which quantile of the junk distribution
// the beat-all floor estimates (R/(R+1) — at 64, the ~98.5th
// percentile) and the evidence per detection (log2(R+1) ≈ 6.02 bits).
// The bar's VALUE stays fully event-measured; R fixes only the ruler's
// resolution.
const ReferenceSampleSize = 64

// minReferenceSample is the cold-start boundary: with fewer scoreable
// references than this, no floor can be estimated and the event runs
// ungated (the budget alone arbitrates — tiny corpora fit in budget
// anyway).
const minReferenceSample = 4

// nullP is the exact rank-based null probability that a background
// chunk scores at or above `score`, given the measured reference
// sample: (1 + #{ref >= score}) / (R + 1). Ties count against the
// candidate (conservative).
func nullP(score float64, refScores []float64) float64 {
	k := 0
	for _, r := range refScores {
		if r >= score {
			k++
		}
	}
	return float64(k+1) / float64(len(refScores)+1)
}

// surprisalBits converts a null probability to evidence-against-noise
// in bits: -log2(p). The shed's value currency — independent channels
// add (semantic and structural detections are measured independent:
// corr(sim, mass) ≈ 0.009 on replay data).
func surprisalBits(p float64) float64 {
	if p <= 0 {
		return math.Inf(1)
	}
	return -math.Log2(p)
}

// confidenceFromBits maps total surprisal back to [0,1] for storage and
// display (EffectiveScore, edge audit): 1 - 2^-bits — the probability
// the candidate is NOT noise, monotone in bits.
func confidenceFromBits(bits float64) float64 {
	if math.IsInf(bits, 1) {
		return 1
	}
	return 1 - math.Exp2(-bits)
}

// bitsFromConfidence inverts confidenceFromBits — assembly recovers the
// shed's bits currency from the stamped confidence.
func bitsFromConfidence(conf float64) float64 {
	if conf >= 1 {
		return math.Inf(1)
	}
	if conf <= 0 {
		return 0
	}
	return -math.Log2(1 - conf)
}

// stanceBits converts LossRatio — the single hand-set value judgment,
// V_harm/(V_gain+V_harm) — into the detection stance s0: the minimum
// evidence-against-noise to act on, and the shed density's zero point.
// LossRatio 0.5 → 1 bit; 0.75 → 2 bits; 0.875 → 3 bits.
func stanceBits(lossRatio float64) float64 {
	if lossRatio <= 0 {
		return 0
	}
	if lossRatio >= 1 {
		return math.Inf(1)
	}
	return -math.Log2(1 - lossRatio)
}

// flatReference reports a degenerate reference sample: zero spread
// means the scoring instrument is not discriminating at all (a
// collapsed scorer emits constants). The caller fails loudly — this is
// the event-local, label-free form of the scorer health check.
func flatReference(refScores []float64) bool {
	if len(refScores) < minReferenceSample {
		return false
	}
	lo, hi := refScores[0], refScores[0]
	for _, r := range refScores[1:] {
		if r < lo {
			lo = r
		}
		if r > hi {
			hi = r
		}
	}
	return hi-lo < 1e-9
}

// referenceSeed derives the deterministic draw seed from the query
// fingerprint: reproducible per event (the determinism suite holds),
// unbiased across events (fingerprints churn with the discourse).
func referenceSeed(fingerprint string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(fingerprint))
	return h.Sum64()
}
