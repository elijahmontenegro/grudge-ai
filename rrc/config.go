package rrc

import "github.com/emontenegr/grudge/rrc/chunk"

// EngineConfig holds tunable parameters for the RRC engine.
//
// Edge formation is gated on raw cross-encoder score (CE). Earlier
// designs fused CE with temporal proximity (WeightCE*CE +
// WeightTemp*temporal); that produced two distinct score
// distributions — same-thread fused was biased upward by the
// temporal term, cross-thread fused had no temporal term — and
// forced a second threshold to compensate. The fix was structural:
// temporal-as-edge-signal duplicates Radius's job (Radius
// unconditionally includes the last N same-thread messages on the
// wire), and outside the Radius window temporal contribution decays
// to negligible. Edge formation is now a pure CE gate; trajectory
// continuity is Radius's exclusive concern.
type EngineConfig struct {
	EdgeThreshold float64 // Absolute edge creation threshold (CE must clear this)
	ScoreFloor    float64 // DAG-traversal cutoff

	// ZScoreThreshold is the adaptive discrimination gate applied
	// per OnMessage batch. After the reranker scores the top-K
	// candidates for a new message, their CE scores form a
	// per-query distribution. Edges only form when a candidate's
	// CE is at least ZScoreThreshold standard deviations above the
	// batch mean. This is the paper's §3.2 Gate 1 adapted to a
	// single-signal substrate: absolute thresholds handle "score
	// too low to matter," z-score handles "doesn't stand out from
	// this query's field." Zero disables the adaptive gate.
	//
	// Why additive to EdgeThreshold: absolute thresholds are
	// brittle under scorer drift (model swap, weight retuning). A
	// candidate that clears an absolute threshold but is
	// indistinguishable from the rest of its batch is not a
	// prerequisite, it's a member of a high-floor cluster. A
	// z-score gate catches that class of false positive without
	// needing the absolute threshold to move.
	ZScoreThreshold float64

	// MinBatchStdDev is the meta-discriminator (paper §3.2 Gate 3):
	// if the per-query CE distribution is too flat
	// (stddev < MinBatchStdDev), the reranker cannot discriminate
	// on this query and the engine returns zero edges rather than
	// picking noise. Per protocol §6.4, Selection SHOULD return
	// nothing rather than low-confidence results. Zero disables.
	MinBatchStdDev float64

	RerankTopK int // Max chunk-pairs sent to the reranker per OnMessage

	// RadiusSize is the last-N window per protocol §2 / §3.3. The
	// Network Regime places Radius between Selected and Current
	// Turn so the model sees continuous recent context bridging
	// deep-history prerequisites and the current Event. Counts
	// messages in the current thread — conversational coherence is
	// thread-local; cross-thread material enters via Selection,
	// not Radius. Radius is also where trajectory continuity
	// lives: same-thread adjacency is preserved unconditionally on
	// the wire, independent of edge scoring.
	RadiusSize int

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
	// (System + Selected + Radius + Current Turn). The assembler
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
	// message to account for chat-template delimiter overhead
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
		EdgeThreshold:   0.60,
		ScoreFloor:      0.3,
		ZScoreThreshold: 0,
		MinBatchStdDev:  0.05,
		RerankTopK: 64,
		RadiusSize: 10,
		Chunk:              chunk.DefaultConfig(),
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
