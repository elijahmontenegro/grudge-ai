package rrc

// EngineConfig holds tunable parameters for the RRC engine.
//
// WeightQUD / QUDExtractionPrompt were removed along with the
// small-fast-model QUD extractor — both were hacks riding on top of the
// classifier. Cross-encoder + temporal is the whole substrate now, and
// the weights still sum to 1.0.
type EngineConfig struct {
	EdgeThreshold float64 // Absolute edge creation threshold (fused score must clear this)
	WeightCE      float64 // Cross-encoder (reranker) weight
	WeightTemp    float64 // Temporal proximity weight
	ScoreFloor    float64 // DAG-traversal cutoff

	// ZScoreThreshold is the adaptive discrimination gate applied
	// per OnMessage batch. After the reranker scores the top-K
	// candidates for a new message, their fused scores form a
	// per-query distribution. Edges only form when a candidate's
	// fused score is at least ZScoreThreshold standard deviations
	// above the batch mean. This is the paper's §3.2 Gate 1 adapted
	// to our fused-score mechanism: absolute thresholds handle
	// "score too low to matter," z-score handles "doesn't stand out
	// from this query's field." Zero disables the adaptive gate.
	//
	// Why additive to EdgeThreshold: absolute thresholds are brittle
	// under scorer drift (model swap, weight retuning). A candidate
	// that clears an absolute threshold but is indistinguishable from
	// the rest of its batch is not a prerequisite, it's a member of a
	// high-floor cluster. A z-score gate catches that class of false
	// positive without needing the absolute threshold to move.
	ZScoreThreshold float64

	// MinBatchStdDev is the meta-discriminator (paper §3.2 Gate 3):
	// if the per-query fused-score distribution is too flat
	// (stddev < MinBatchStdDev), the reranker cannot discriminate
	// on this query and the engine returns zero edges rather than
	// picking noise. Per protocol §6.4, Selection SHOULD return
	// nothing rather than low-confidence results. Zero disables.
	MinBatchStdDev float64

	RerankTopK int // Max chunk-pairs sent to the reranker per OnMessage

	// Radius is the last-N window per protocol §2 / §3.3. The Network
	// Regime places Radius between Selected and Current Turn so the
	// model sees continuous recent context bridging deep-history
	// prerequisites and the current Event. Counts messages in the
	// current thread — conversational coherence is thread-local;
	// cross-thread material enters via Selection, not Radius.
	//
	// Radius plays a different role than trajectory signal in
	// Selection. Radius is unconditional rolling context preservation
	// on the wire; trajectory contribution inside FuseScore is a
	// prerequisite-detection signal scored against EdgeThreshold.
	// The two are complementary, not redundant: Radius guarantees
	// near-term messages are present regardless of Selection, and
	// trajectory signal lets older same-thread messages qualify as
	// prerequisites when they carry enough combined signal to clear
	// the threshold.
	RadiusSize int

	// Chunk controls how long messages are split for embedding and
	// cross-encoder scoring. Message-level scoring silently truncated
	// at the model's 512-token boundary under the old classifier,
	// which made anything past ~2KB invisible to RRC (paper §6.8
	// "Cross-Encoder Input Truncation"). Chunking is per protocol
	// §6.5 implementation freedom — messages remain the graph unit,
	// chunks are the scoring substrate; chunk-pair scores aggregate to
	// message-pair edges via max-merge.
	Chunk ChunkConfig

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

	// NLIFusionWeight is α in the composite scoring fusion:
	// fused = α · bge_rerank_score + (1-α) · nli_entailment_score.
	// At α=1 fusion degenerates to bge-only (today's behavior); at
	// α=0 to NLI-only. 0.5 balances the two — bge captures surface
	// relevance well, NLI captures the directional dependency
	// ("this content answers that query") that bge-reranker-v2-m3
	// misses, surfacing prerequisite content over process-thinking
	// that merely shares query language. Takes effect only when an
	// Entailer is wired on the Engine; otherwise ignored.
	NLIFusionWeight float64
}

// DefaultConfig returns the default engine configuration.
//
// EdgeThreshold 0.35 / ScoreFloor 0.01 are the calibrated baseline
// from the prior NLI-to-reranker migration. Tuning these needs
// startup-time invalidation (filter loaded edges against current
// threshold) to be meaningful across existing threads — without
// it, config changes only bite new threads, producing workflow
// friction and inconsistent experiments. Leaving these at the
// known baseline until invalidation lands.
//
// WeightCE 0.6, WeightTemp 0.4: both axes of prerequisite signal
// get meaningful weight in the fused score.
//
//   - Semantic axis (reranker): "this content is about the same
//     thing the current turn is producing."
//   - Structural axis (temporal proximity): "this is what the
//     current turn is continuing from."
//
// Both are legitimate prerequisite indicators and neither subsumes
// the other. A focused autonomous run flattens reranker signal
// across the on-topic corpus — temporal distinguishes which
// priors the current turn actually continues from. A sharp topic
// pivot can make the immediately-prior turn look semantically
// irrelevant when it's structurally critical — temporal keeps it
// in.
//
// The weights combine linearly in FuseScore. Edges require the
// fused score to clear EdgeThreshold (§6.4 discriminativity). No
// trajectory-as-override clause — every edge earns its place via
// the combined signal.
func DefaultConfig() EngineConfig {
	return EngineConfig{
		// EdgeThreshold 0.5 / ScoreFloor 0.3. Tuned for the protocol's
		// discrimination contract (§6.4): "Selection SHOULD return
		// nothing rather than return low-confidence results." Observed
		// fused-score distribution on a 9.4k-edge corpus: ~78% of edges
		// have fused score < 0.3 (noise shoulder), the confident-
		// prerequisite population sits above 0.5. EdgeThreshold at 0.5
		// only admits confident edges into the DAG. ScoreFloor at 0.3
		// trims multi-hop walks once the multiplicative decay crosses
		// into the noise band. Earlier baseline (0.35 / 0.01) was
		// calibrated against an older NLI classifier's output range
		// and became wildly permissive under the bge-reranker-v2-m3
		// distribution — Selection routinely returned 50%+ of the
		// corpus, which is the opposite of hyperselection. These new
		// values produce 5-30 selections per query on the same corpus.
		EdgeThreshold:   0.5,
		WeightCE:        0.6,
		WeightTemp:      0.4,
		ScoreFloor:      0.3,
		ZScoreThreshold: 1.0,
		MinBatchStdDev:  0.05,
		RerankTopK:      64,
		RadiusSize:      10,
		Chunk:           DefaultChunkConfig(),
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
		// Balanced fusion between bge-rerank (surface relevance)
		// and NLI (directional dependency). Takes effect only if
		// the engine has an Entailer wired.
		NLIFusionWeight: 0.5,
	}
}
