package rrc

import (
	"github.com/elijahmontenegro/grudge/rrc/calibrate"
	"github.com/elijahmontenegro/grudge/rrc/chunk"
)

// EngineConfig holds tunable parameters for the RRC engine.
//
// Edge formation is gated on raw cross-encoder score (CE). Earlier
// designs fused CE with temporal proximity (WeightCE*CE +
// WeightTemp*temporal); that produced two distinct score
// distributions — same-thread fused was biased upward by the
// temporal term, cross-thread fused had no temporal term — and
// forced a second threshold to compensate. The fix was structural:
// temporal-as-edge-signal duplicates Local Context's job (bounded
// same-thread discourse is already on the wire), and outside that
// bounded context temporal contribution decays
// to negligible. Edge formation is now a pure CE gate; trajectory
// continuity is Local Context's concern.
type EngineConfig struct {
	// Calibrator maps the raw signals (semantic similarity, structural
	// descendant mass) into one currency — P(prereq | sim, mass) — so
	// acceptance is calibrated expected value against the token budget's
	// marginal price, not a flat threshold on a raw score. This is the A4
	// acceptance mechanism; it supersedes EdgeThreshold/ScoreFloor (below,
	// retained only for settings/telemetry compatibility, no longer gating).
	// The default is a bootstrap calibrator (DefaultConfig) that reproduces
	// the precision-first operating point until a fitted model from the
	// rrc/calibrate offline pipeline replaces it per-deployment.
	Calibrator calibrate.Calibrator

	// LossRatio is the precision stance — V_harm/(V_gain+V_harm) — the
	// single honest hand-set scalar in the acceptance mechanism. It is a
	// value judgment (how much a wasted token is hated vs. a hallucinated
	// inclusion), not derivable from data, and it does NOT move on scorer
	// swap. With shadow price μ=0 (budget slack) the acceptance floor is
	// P(prereq) ≥ LossRatio. Higher = more precision-first (RRC's stance).
	LossRatio float64

	// Deprecated: retained for settings-file / GraphQL compatibility and
	// telemetry, no longer used for acceptance gating (A4 replaced the flat
	// cutoffs with calibrated expected value + budget shed). EdgeThreshold
	// was the flat CE cutoff; ScoreFloor the DAG-traversal cutoff.
	EdgeThreshold float64 // Deprecated: no longer gates edge formation.
	ScoreFloor    float64 // Deprecated: no longer gates DAG traversal.

	// Deprecated: the z-score "relative standout" gate was removed in A4.
	// It was a statistical patch for a flat threshold's brittleness under
	// scorer drift; calibrated P(prereq) subsumes that job (the fusion
	// coefficients re-fit on drift, so there is nothing for a z-gate to
	// compensate). Retained only for settings-file / GraphQL compatibility;
	// no longer gates edge formation.
	ZScoreThreshold float64

	// MinBatchStdDev is the meta-discriminator (paper §3.2 Gate 3):
	// if the per-call CE distribution is too flat
	// (stddev < MinBatchStdDev), the reranker cannot discriminate
	// for this Local Context and the engine returns zero edges rather than
	// picking noise. Per protocol §6.4, Selection SHOULD return
	// nothing rather than low-confidence results. Zero disables.
	MinBatchStdDev float64

	RerankTopK int // Max eligible candidate chunks reranked per Local Context chunk

	// LocalContextSize bounds the recent same-thread messages used
	// to construct both serialized scorer input and the
	// provider-native continuation payload. Cross-thread material
	// enters through Selection.
	LocalContextSize int

	// Chunk controls how long messages are split for embedding and
	// cross-encoder scoring. Message-level scoring silently truncated
	// at the model's 512-token boundary under the old scorer,
	// which made anything past ~2KB invisible to RRC (paper §6.8
	// "Cross-Encoder Input Truncation"). Chunking is per protocol
	// §6.5 implementation freedom — messages remain the graph unit,
	// chunks are the scoring substrate; chunk-pair scores aggregate to
	// message-pair edges via max-merge.
	Chunk chunk.Config

	// ContextBudgetTokens is the target cap for the assembled prompt
	// (System + Selected + Local Context). The assembler
	// pre-sizes Selected against this budget before sending: if the
	// token estimate exceeds, lowest-score Selected entries are
	// dropped until the estimate fits. This moves shed-to-fit from
	// a reactive round-trip loop to a proactive single-pass
	// computation — the reactive path remains as a safety net for
	// when the estimator undercounts, not as steady-state traffic.
	//
	// Zero disables proactive budgeting entirely; the reactive
	// overflow loop (now with exponential shed step) still runs as
	// the safety net.
	//
	// Set conservatively relative to the model's advertised context
	// so there's headroom for: the provider's own system-prompt
	// overhead, tool-declaration blocks, max output tokens, and the
	// inherent imprecision of character-based token estimation.
	ContextBudgetTokens int

	// DiversityLambda is the MMR tradeoff between relevance and
	// diversity at post-Selection emission. effective(C) =
	// λ·origScore(C) - (1-λ)·max_sim(C, already_kept). At λ=1 MMR
	// degenerates to score-desc (today's behavior); at λ=0 it picks
	// purely for diversity with no regard for relevance. 0.7 is the
	// standard default from Carbonell & Goldstein (1998) — mostly
	// relevance-driven but with enough diversity penalty to collapse
	// near-duplicate chains (the "nine copies of let me check the
	// chapter" pattern) to one or two representatives.
	DiversityLambda float64

	// BudgetHeadroomPct is a fixed global margin on the context
	// budget. estimate ≤ ContextBudgetTokens × BudgetHeadroomPct.
	// Absorbs tokenizer divergence (cl100k_base proxy vs the real
	// model's BPE), chat-template preambles, and server-side
	// wrapping without claiming to know any of them. One knob,
	// globally tunable; per-model calibration is explicitly out of
	// scope (best-effort estimate). 0.90 means we target 90% of
	// the advertised budget, holding 10% in reserve for the
	// inherent imprecision of a cross-tokenizer estimator. Zero
	// disables the margin (estimate compared directly to budget).
	BudgetHeadroomPct float64

	// PerMsgDelimiterTokens is a fixed small constant added per
	// message to approximate chat-template delimiter overhead
	// (ChatML `<|im_start|>` etc., Llama 3 headers, Mistral
	// `[INST]` pairs). Stable across templates — ChatML ~4, Llama 3
	// ~5, Mistral ~4. Observed budget estimate has been under-
	// counting message-count-proportionally without this; with a
	// 40-message wire that's ~200 tokens.
	PerMsgDelimiterTokens int
}

// DefaultConfig returns the default engine configuration.
//
// EdgeThreshold = 0.60 — calibrated 2026-05-24 against the 31-triple
// labeled eval set (eval/pairs.json) after the zerank adapter's
// binary-logit scoring fix. At 0.60: precision = 100%, recall =
// 45.2%. The 0.60 floor sits in a clean gap between the top-scoring
// negative (0.562) and the bottom-scoring recognized positive
// (0.660). Stricter floors (0.65, 0.70) keep 100% precision but
// trade recall; looser floors (0.55) start admitting false positives
// (top negative is 0.562). The 55% recall ceiling reflects model
// capability — zerank is an IR-relevance reranker, not a discourse
// or coreference model, so D_coreference / C_factual_recall pairs
// score zero regardless of threshold. See docs/eval-reports for the
// sweep table.
//
// ZScoreThreshold = 0 (disabled). The score distribution under the
// fixed scorer is bimodal: negatives cluster at 0.000, positives at
// 0.66–0.96. Z-score over a bimodal is degenerate, and within the
// positive cluster the relative-standout test strips most edges as
// "not unusually high" — fighting the abs floor for the same job.
// One discriminator (the abs floor) is the principled architecture.
//
// MinBatchStdDev = 0.05 retained as a separate sanity guard: if the
// reranker returns near-identical scores across every candidate in
// a batch, it's signaling "I can't discriminate this batch" and the
// engine drops edges for that round only. Independent of edge
// scoring; not redundant with z-gate.
//
// ScoreFloor = 0.3 is unchanged; it's a lower-bound on what edges
// can reach into Selection at assembly time, not edge formation.
func DefaultConfig() EngineConfig {
	return EngineConfig{
		// Bootstrap calibrator + loss ratio. Until the rrc/calibrate offline
		// pipeline fits real coefficients against counterfactual-coherence
		// labels for a deployment's scorer, this reproduces the shipped
		// precision-first operating point: with μ=0 (budget slack) acceptance
		// is P(prereq|sim,mass) ≥ LossRatio(0.5). The bootstrap sigmoid is
		// steep in similarity centered near the old 0.60 floor
		// (A·0.60 + C ≈ 0 → P ≈ 0.5), so a candidate at sim=0.60/mass=0 sits
		// right at the accept boundary — matching the retired EdgeThreshold.
		// The positive mass term (B) lets a provenance-reached root clear the
		// boundary at low similarity, which the flat cutoff never could — the
		// whole point of the /\. These are a bootstrap, NOT a tuned magic
		// number: the empirical flip is a fitted Calibrator, not a re-hunt of
		// a threshold. See docs/JOURNAL and the structural-lift design.
		Calibrator: calibrate.Bootstrap(0.60, 12.0, 6.0),
		LossRatio:  0.5,

		EdgeThreshold:    0.60,
		ScoreFloor:       0.3,
		ZScoreThreshold:  0,
		MinBatchStdDev:   0.05,
		RerankTopK:       64,
		LocalContextSize: 10,
		Chunk:            chunk.DefaultConfig(),
		// Safety-net boundary for the Network payload. Measured in
		// "approximate tokens" — specifically (UTF-8 rune count)/4,
		// a rough English-prose heuristic, not an actual tokenizer
		// output. This is deliberately a proxy: we do not commit to
		// per-provider tokenizers (dependency weight, provider drift)
		// and paid /tokenize endpoints (latency, cost). The budget's
		// role per protocol §3.3 is to enforce Network-regime
		// overflow resolution — when the estimated assembly exceeds,
		// the assembler sheds lowest-score Selected entries. The
		// provider's 400 response remains the ground truth for
		// "actually fits" via the reactive exponential shed layer.
		//
		// 150000 corresponds to ~600k characters, sized for 200k-
		// context models (Claude, minimax-m2.7) with headroom for
		// provider overhead + tool declarations + output reserve.
		// For 128k-context models (GPT-4o, Llama 3.1) this is too
		// aggressive on paper; reactive shed catches the residual.
		ContextBudgetTokens: 150000,

		// MMR diversity default per Carbonell & Goldstein (1998).
		DiversityLambda: 0.7,
		// 10% margin absorbs cl100k_base-vs-real-tokenizer drift,
		// template preambles, and other observable byte-level
		// accounting gaps that aren't worth enumerating individually.
		BudgetHeadroomPct: 0.90,
		// 5 tokens/message covers ChatML / Llama 3 / Mistral role
		// delimiters to within ±1.
		PerMsgDelimiterTokens: 5,
	}
}
