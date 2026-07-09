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
	// acceptance mechanism. The default is a bootstrap calibrator
	// (DefaultConfig) that reproduces the precision-first operating point
	// until a fitted model replaces it per-deployment (the runtime
	// self-fits from the embedded seed set on first boot per scorer).
	Calibrator calibrate.Calibrator

	// ScorerModelID names the instrument whose units every formed edge's
	// observations are in (raw similarity, provenance contribution
	// weights) — stamped onto edges at formation as an observation
	// attribute, exactly as the scores cache keys by model. Stamping is
	// the retroactively-unrecoverable part; consumption protocols for a
	// mixed-instrument corpus derive from stamped data if a scorer swap
	// ever occurs (re-MEASURE foreign-unit observations, never rewrite
	// them). "" (tests, benches) stamps nothing.
	ScorerModelID string

	// LossRatio is the precision stance — V_harm/(V_gain+V_harm) — the
	// single honest hand-set scalar in the acceptance mechanism. It is a
	// value judgment (how much a wasted token is hated vs. a hallucinated
	// inclusion), not derivable from data, and it does NOT move on scorer
	// swap. With shadow price μ=0 (budget slack) the acceptance floor is
	// P(prereq) ≥ LossRatio. Higher = more precision-first (RRC's stance).
	LossRatio float64

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
	// overflow loop (which sheds one delivery group per provider
	// round trip) still runs as the safety net.
	//
	// This is the caller's statement of the model's usable window
	// (prompt + generation): callers forward it to the provider where
	// the serving window is caller-controlled (ollama num_ctx), and
	// convert it into counter units via a learned per-model scale
	// grounded in provider-reported usage. Set it to the model's real
	// context size, reserving max output tokens via BudgetHeadroomPct.
	ContextBudgetTokens int


	// BudgetHeadroomPct is a fixed global margin on the context
	// budget. estimate ≤ ContextBudgetTokens × BudgetHeadroomPct.
	// A pure safety margin: output-token reserve plus residual
	// estimation noise (token-boundary effects at message joins,
	// template deltas the projection approximates). Tokenizer-scale
	// divergence is NOT its job — that is corrected by the learned
	// per-model scale (rrc/tokenscale), grounded in the provider's
	// own reported usage. 0.90 targets 90% of the window, holding
	// 10% for generation and noise. Zero disables the margin.
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
// MinBatchStdDev = 0.05 is a sanity guard: if the reranker returns
// near-identical scores across every candidate in a batch, it's
// signaling "I can't discriminate this batch" and the engine drops
// edges for that round only. Independent of edge scoring.
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

		MinBatchStdDev:   0.05,
		RerankTopK:       64,
		LocalContextSize: 10,
		Chunk:            chunk.DefaultConfig(),
		// Boundary for the Network payload, in the configured
		// counter's units (chunk.Config.Estimator; the app installs
		// cl100k BPE). The counter is a lookalike, not the model's
		// tokenizer — its absolute scale is grounded per model by
		// rrc/tokenscale against provider-reported usage, so exact
		// per-provider tokenizers are deliberately not a dependency.
		// The budget's role per protocol §3.3 is Network-regime
		// overflow resolution: when the estimated assembly exceeds,
		// the assembler sheds lowest-score Selected entries. The
		// provider's reported usage remains the ground truth for
		// "actually fits", with the reactive one-group-per-retry
		// shed loop as the last-resort floor.
		//
		// 150000 is sized for 200k-context models (Claude,
		// minimax-m2.7) with headroom for provider overhead + tool
		// declarations + output reserve. For 128k-context models
		// (GPT-4o, Llama 3.1) set it to the real window.
		ContextBudgetTokens: 150000,

		// MMR diversity default per Carbonell & Goldstein (1998).
		// 10% margin holds output reserve + residual estimation
		// noise; tokenizer-scale drift is the learned scale's job.
		BudgetHeadroomPct: 0.90,
		// 5 tokens/message covers ChatML / Llama 3 / Mistral role
		// delimiters to within ±1.
		PerMsgDelimiterTokens: 5,
	}
}
