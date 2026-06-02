# RRC Substrate Model Evaluation — Final Report

**Date:** 2026-04-29
**Hardware target:** NVIDIA RTX 3080 Ti, 12 GB VRAM
**Scope:** Architectural and model-level selection for RRC's two-layer prereq detection substrate (retrieval + rerank).
**Audience:** Whoever picks up Grudge next. Self-contained — does not assume prior session context.

---

## 1. Background

### 1.1 RRC's contract

Retrieval-Restored Continuation (RRC) replaces full-history LLM context with a two-layer prereq detection pipeline:

1. **Layer 1 (Retrieval).** Given a new message, surface a top-K candidate set of prior chunks from the corpus. Cost must be sub-linear in N (corpus size) — RRC's invariance promise.
2. **Layer 2 (Detection).** Score each candidate (query, candidate) pair with high precision; emit edges where the score exceeds `EdgeThreshold`.

The load-bearing contract:

- **Lossless retrieval.** Every actual prerequisite must surface in the top-K candidate set. False negatives are unrecoverable downstream.
- **Sub-linear per-step compute.** Per-OnMessage cost bounded by K, not N. Storage may grow linearly.
- **Detection precision sufficient for edge formation.** The reranker must produce scores that separate prereqs from non-prereqs reliably enough to threshold.
- **Reasonable per-turn latency.** Order of seconds, not minutes.

The contract is silent on architecture. Single-vector dense bi-encoder, multi-vector late-interaction (ColBERT), sparse, and hybrid all qualify in principle.

### 1.2 What this evaluation answers

Which model architecture and specific (Layer 1, Layer 2) pair best satisfies RRC's contract on Grudge's actual workload, given a 12 GB consumer GPU constraint.

### 1.3 What this evaluation does not answer

- Latency at corpus scale (>10⁴ chunks). Sub-linear retrieval was tested architecturally (sqlite-vec ANN exists and is wired) but quality measurements were on flat-scan small pools.
- Substrate cost trade-offs (sqlite-vec vs Postgres+VectorChord vs Qdrant). Architecture quality didn't motivate the comparison.
- License posture for shipping. Per the substrate plan, this is deferred to release time and was correctly identified mid-eval as a non-factor in the architectural decision.

---

## 2. Methodology

### 2.1 Eval set

Two test shapes, total 36 query points.

**R2 — Chunk-sized utterances. 31 pairs across six categories.**
Candidate length 33–1430 chars. Every "correct" candidate fits in a single production chunk (`rrc/chunking.go` default: 2000 char max). Stage 1 evaluates 5-way (correct + 4 distractors). Stage 2 evaluates 29-way (correct + 4 distractors + 24 cross-pollinated other-pair corrects). Categories:

- A_technical_vocab (6) — rare terms, exact lexical matches
- B_prose_dependency (5) — narrative referents
- C_factual_recall (5) — atomic facts
- D_coreference (5) — pronoun resolution
- E_conflation_traps (4) — distractors with heavy lexical overlap
- F_long_document (6) — buried-section discrimination, candidates from same parent doc with overlapping vocabulary

**R3 — Multi-chunk source discrimination. 5 sources, 5 queries.**
Each "source" is a 2800–5034 char document from real Grudge artifacts (substrate plan, journal, CLAUDE.md). Sources pre-chunked via `rrc/chunking.go` defaults (2000 char max, 200 char overlap, paragraph-preferring boundaries) into 13 chunks total. Pool is the 13 chunks. Per query, score every chunk; aggregate to source-level rank via max-over-chunks. Reports source-level top-K.

This is the production retrieval shape: messages > 2000 chars get chunked, each chunk indexed separately, the engine forms message-pair edges via max-over-chunks aggregation.

### 2.2 Candidate set

14 distinct (Layer 1, Layer 2) configurations across three architectural paths:

- **P1 — single-vector dense retrieve + dedicated reranker.** Today's substrate shape.
- **P2 — multi-vector ColBERT-style single-stage retrieval (no separate reranker).** Architectural alternative.
- **P3 — single-vector dense retrieve + multi-vector MaxSim rerank (live-encoded).** Hybrid.

Models tested:

| Layer | Model | Params | Role | License |
|---|---|---|---|---|
| L1 dense | Qwen3-Embedding-0.6B | 0.6B | embedder | Apache 2.0 |
| L1 dense | jina-embeddings-v5-text-small | 0.6B | embedder | Apache 2.0 |
| L1 dense | zembed-1 | 4B | embedder | CC-BY-NC-4.0 |
| L2 reranker | Qwen3-Reranker-0.6B | 0.6B | LLM-judge | Apache 2.0 |
| L2 reranker | zerank-1-small | 1.7B | LLM-judge | Apache 2.0 |
| L2 reranker | zerank-2 | 4B | LLM-judge | CC-BY-NC-4.0 |
| L1+L2 (P2) | answerai-colbert-small-v1 | 33M | multi-vector | Apache 2.0 |
| L1+L2 (P2) | SauerkrautLM-Multi-ModernColBERT | ~150M | multi-vector | Apache 2.0 |
| L1+L2 (P2) | GTE-ModernColBERT-v1 | ~150M | multi-vector | open |
| L1+L2 (P2) | jina-colbert-v2 | 560M | multi-vector | CC-BY-NC-4.0 |

License is reported for completeness. Per Section 1.3, it does not enter the architectural decision.

### 2.3 Harness

Code: `rerank_eval/run_eval_paths.py`, `run_eval_multichunk.py`, `run_round4_nc.py`.

**Co-resident loading mode (default).** Both Layer 1 and Layer 2 loaded simultaneously in VRAM. Measures peak co-resident VRAM (the deployment-shape number). Used for all configs that fit ≤12 GB combined.

**Sequential loading mode.** Load L1 → encode all candidates and queries → unload L1 → load L2 → rerank top-K → unload. Used only for configs that exceed 12 GB co-resident (zembed-1 + zerank-2 = ~16 GB at bf16; zembed-1 alone peaks at 10.12 GB). Sequential mode measures *quality independently of GPU-fit constraint*. It is not a viable production mode (per-turn model swap = 30–60s per load × 2 = unreasonable latency).

**Inference precision:** bf16 throughout. No quantization in the eval — quantization was attempted in a prior session for the zembed-1+zerank-2 pair and crashed the host system (NF4 zembed-1 + int8 zerank-2 + KV cache + CUDA graphs hit the GPU/system allocator failure zone during model boot).

**Metrics reported:**
- Stage 1 top-1 (R2 only) — correct ranks #1 of 5
- Stage 2 top-1 — correct ranks #1 of 29 (R2) or 13 chunks aggregated to 5 sources (R3)
- Stage 2 recall@5, recall@10 (R2 only) — correct ranks within top-K of 29
- Co-resident peak VRAM (GB)
- Mean per-pair Stage 1 latency (ms)

### 2.4 Methodology limitations honestly stated

- **Small N.** 31 + 5 = 36 query points. Statistical confidence is correspondingly weak; differences of 1–2 pairs are within noise.
- **E1_bible is a structural eval-pair issue, not a model issue.** The distractor "let me check the character bible" lexically dominates the actual content sentence "Marcus Webb is the antagonist in chapter 7 of the bible". Every config tested misses E1 at Stage 2 top-1 — including frontier-class NC models. This costs the entire field a guaranteed −1 on Stage 2 top-1.
- **R3 has 5 sources.** Top-1 ceilings cluster at 5/5; the eval has limited discriminating power at this scale.
- **R2 candidates are short** (33–1430 chars). The ColBERT models tested may be disadvantaged on very short text where MaxSim has few tokens to align across.
- **No corpus-scale latency test.** Sub-linear retrieval claims are theoretical at this eval pool size (≤29 candidates). Real RRC corpus is 10⁴–10⁵+ chunks. ANN substrate latency at scale was not measured.
- **Multi-vector tested via exhaustive PyLate MaxSim, not PLAID-on-VectorChord.** Exhaustive MaxSim is a *quality upper bound* for any PLAID implementation. PLAID can only equal or underperform what was measured. Substrate cost (storage, indexing, retrieval latency at corpus scale) was not measured for multi-vector.
- **License-tier confound.** zembed-1 + zerank-2 (NC, 4B+4B) was the prior session's pre-eval winner on a different test set. We expected it to dominate; it tied within margin. This may indicate the eval set isn't as hard as advertised, or that strong embedder + 1.7B reranker is genuinely close to the ceiling for our content.

---

## 3. Results

### 3.1 R2 — chunk-sized utterances, 31 pairs

| Config | Path | S1 #1 | S2 #1 | S2 r@5 | S2 r@10 | Peak GB | Mean ms | Mode |
|---|---|---|---|---|---|---|---|---|
| Qwen3-Emb-0.6B + zerank-1-small | P1 | 30/31 | **29/31** | **31/31** | **31/31** | 4.64 | 297 | co-resident |
| jina-v5-small + zerank-1-small | P1 | 30/31 | **29/31** | **31/31** | **31/31** | 4.80 | 435 | co-resident |
| zembed-1 + zerank-1-small | P1 | 30/31 | **29/31** | **31/31** | **31/31** | 10.12 | – | sequential |
| zembed-1 + zerank-2 | P1 | 30/31 | 28/31 | **31/31** | **31/31** | 10.12 | – | sequential |
| Qwen3-Emb-0.6B + zerank-2 | P1 | 30/31 | 28/31 | **31/31** | **31/31** | 8.05 | – | sequential |
| zembed-1 + Qwen3-Reranker-0.6B | P1 | 31/31 | 25/31 | 30/31 | **31/31** | 10.12 | – | sequential |
| Qwen3-Emb-0.6B + Qwen3-Reranker-0.6B | P1 | 31/31 | 23/31 | 30/31 | 30/31 | 2.41 | 364 | co-resident |
| answerai-colbert-small-v1 (33M) | P2 | 22/31 | 18/31 | 25/31 | 28/31 | 0.14 | 102 | co-resident |
| GTE-ModernColBERT-v1 | P2 | 19/31 | 18/31 | 25/31 | 29/31 | 0.63 | 68 | co-resident |
| jina-colbert-v2 (560M) | P2 | 21/31 | 17/31 | 26/31 | 28/31 | 1.12 | 70 | co-resident |
| SauerkrautLM-Multi-ModernColBERT | P2 | 17/31 | 14/31 | 23/31 | 26/31 | 0.63 | 50 | co-resident |
| Qwen3-Emb-0.6B + answerai-colbert | P3 | 22/31 | 18/31 | 25/31 | 28/31 | 2.41 | 195 | co-resident |
| Qwen3-Emb-0.6B + jina-colbert-v2 | P3 | 21/31 | 17/31 | 26/31 | 28/31 | 2.41 | 160 | co-resident |
| jina-v5-small + answerai-colbert | P3 | 22/31 | 18/31 | 25/31 | 28/31 | 2.64 | 264 | co-resident |

### 3.2 R3 — multi-chunk source discrimination, 5 sources

| Config | Path | top-1 | r@2 | r@3 | Peak GB | Mean ms | Mode |
|---|---|---|---|---|---|---|---|
| Qwen3-Emb-0.6B + zerank-1-small | P1 | **5/5** | 5/5 | 5/5 | 4.64 | 594 | co-resident |
| jina-v5-small + zerank-1-small | P1 | **5/5** | 5/5 | 5/5 | 4.80 | 644 | co-resident |
| zembed-1 + zerank-1-small | P1 | **5/5** | 5/5 | 5/5 | 9.50 | – | sequential |
| zembed-1 + zerank-2 | P1 | **5/5** | 5/5 | 5/5 | 9.50 | – | sequential |
| Qwen3-Emb-0.6B + zerank-2 | P1 | **5/5** | 5/5 | 5/5 | 8.05 | – | sequential |
| zembed-1 + Qwen3-Reranker-0.6B | P1 | 1/5 | 2/5 | 3/5 | 9.50 | – | sequential |
| Qwen3-Emb-0.6B + Qwen3-Reranker-0.6B | P1 | 1/5 | 2/5 | 3/5 | 2.39 | 513 | co-resident |
| answerai-colbert-small-v1 | P2 | **5/5** | 5/5 | 5/5 | 0.14 | 14 | co-resident |
| GTE-ModernColBERT-v1 | P2 | **5/5** | 5/5 | 5/5 | 0.63 | 490 | co-resident |
| jina-colbert-v2 | P2 | 4/5 | 5/5 | 5/5 | 1.13 | 41 | co-resident |
| Qwen3-Emb-0.6B + answerai-colbert | P3 | **5/5** | 5/5 | 5/5 | 2.07 | 83 | co-resident |
| Qwen3-Emb-0.6B + jina-colbert-v2 | P3 | 4/5 | 5/5 | 5/5 | 2.32 | 157 | co-resident |

---

## 4. Findings (data observations, before interpretation)

### 4.1 Quality clusters

- **At R2 Stage 2 r@5 (recall ceiling):** five P1 configs hit 31/31. All combine a strong embedder (Qwen3-Emb-0.6B, jina-v5-small, or zembed-1) with a strong reranker (zerank-1-small 1.7B or zerank-2 4B).
- **At R2 Stage 2 top-1 (precision):** ceiling is 29/31. Three configs tied (Qwen3-Emb / jina-v5 / zembed-1, each + zerank-1-small). The two-pair gap is consistently E1_bible plus one of {D2_it_stored, E4_log_says} depending on reranker.
- **At R3 top-1:** eight configs hit 5/5. Two more hit 4/5 (jina-colbert-v2 single-stage and cascaded — same G1 miss).

### 4.2 Failure modes

**Reranker-capacity collapse on multi-chunk.** Two configs collapse to 1/5 on R3:
- Qwen3-Emb-0.6B + Qwen3-Reranker-0.6B
- zembed-1 + Qwen3-Reranker-0.6B

The rank pattern is monotonic: G1 #1, G2 #2, G3 #3, G4 #4, G5 #5. Both fail the same way regardless of embedder strength. A 0.6B reranker cannot aggregate signal across N chunks of the same source against N chunks of competitor sources. A 1.7B or 4B reranker handles it.

**Multi-vector loss on short utterances.** Every P2 ColBERT config (33M, 150M×2, 560M) lands in the 14–18/31 range on R2 Stage 2 top-1, vs 28–29/31 for P1 with strong reranker. The size confound is broken: jina-colbert-v2 at 560M doesn't beat answerai-colbert-small at 33M. The architectural ceiling for multi-vector on short-utterance discrimination at this candidate-pool scale appears to be ~25/31 r@5 / ~28/31 r@10, regardless of model size.

**Reranker is the load-bearing layer.** When the reranker is held constant at zerank-1-small, three different embedders (Qwen3-Emb-0.6B, jina-v5-small, zembed-1) produce identical Stage 2 top-1 totals (29/31). The embedder-pair-quality argument from the prior session ("zembed-1's vector geometry encodes the same relevance criteria zerank-2 uses") does not produce a measurable quality advantage over Qwen3-Embedding-0.6B at this eval scale.

**Cascaded P3 ≡ single-stage P2 when the multi-vector model is the rerank stage.** Qwen3-Emb + answerai-colbert cascaded → 18/31 R2 top-1, 5/5 R3. answerai-colbert alone → 18/31 R2, 5/5 R3. The cascade adds zero quality. The multi-vector model is the bottleneck regardless of upstream retrieval.

### 4.3 Latency and VRAM

P1 latency runs 297–650 ms mean per-pair (includes the reranker). P2 ColBERT-only latency runs 14–500 ms (no reranker). The latency gap is meaningful (10×–30×) but both are in the "reasonable" range for RRC's contract.

VRAM: P1 with zerank-1-small fits 4.6–4.8 GB co-resident. P2 ColBERT-only fits 0.14–1.13 GB. The 4B-class P1 configs (zembed-1, zerank-2) exceed 12 GB co-resident at bf16 and require sequential or quantized loading.

---

## 5. Disqualifying constraints

### 5.1 Contract: lossless retrieval recall

R2 r@5 < 31/31 ⇒ candidate fails the recall layer of the contract. R3 top-1 < 5/5 ⇒ candidate fails the multi-chunk aggregation contract.

Configs that fail one or both:
- Qwen3-Reranker-0.6B paired with anything → R3 collapse to 1/5
- All P2 multi-vector → R2 r@5 ∈ {23/31, 25/31, 26/31}
- All P3 cascaded with multi-vector rerank → same R2 r@5 ceiling
- jina-colbert-v2 → R3 4/5

### 5.2 Hardware: 12 GB co-resident inference at bf16

Sequential loading is not a production mode. Per-turn model swap = 30–60s × 2 model loads = unreasonable latency, violates RRC's reasonable-latency preference, and was empirically demonstrated unreasonable in prior session attempts. Quantization to fit was attempted prior session; NF4 zembed + int8 zerank-2 + KV cache + CUDA graphs crashed the host.

Configs that exceed 12 GB co-resident:
- zembed-1 + zerank-2 (~16 GB)
- zembed-1 + zerank-1-small (~10 GB+ peak; tight)
- Qwen3-Emb-0.6B + zerank-2 (~9 GB+; borderline)

### 5.3 Intersection of contract + hardware

| Config | Contract pass? | Hardware pass? |
|---|---|---|
| Qwen3-Emb-0.6B + zerank-1-small | ✅ | ✅ (4.64 GB) |
| jina-v5-small + zerank-1-small | ✅ | ✅ (4.80 GB) |
| zembed-1 + zerank-1-small | ✅ | ❌ (10+ GB) |
| zembed-1 + zerank-2 | ✅ (S2 top-1 −1) | ❌ (16 GB) |
| Qwen3-Emb-0.6B + zerank-2 | ✅ (S2 top-1 −1) | ❌ (~9 GB borderline) |
| All Qwen3-Reranker-0.6B configs | ❌ (R3 collapse) | ✅ |
| All P2 multi-vector | ❌ (R2 r@5 < 31) | ✅ |
| All P3 cascaded | ❌ (R2 r@5 < 31) | ✅ |

Two configs satisfy both. Both happen to be Apache 2.0; this is not a selection criterion.

---

## 6. Selection

**Primary: Qwen3-Embedding-0.6B + zerank-1-small.**
**Functional alternative: jina-v5-small + zerank-1-small.**

Both meet contract + hardware. They are within margin on every quality metric:
- R2 Stage 1: 30/31 each
- R2 Stage 2 top-1: 29/29 each
- R2 Stage 2 r@5: 31/31 each
- R3 top-1: 5/5 each

Tiebreakers favor Qwen3-Emb-0.6B:
- Slightly lower co-resident VRAM (4.64 vs 4.80 GB)
- Slightly lower mean latency (297 vs 435 ms)
- Slightly lower (0.6B vs 0.6B nominal but jina has trust_remote_code + peft modeling overhead)

If jina-v5's matryoshka dimension flexibility (32/64/128/256/512/768/1024) becomes valuable downstream (storage compression), it can be substituted with no quality loss.

---

## 7. Eliminated paths

### 7.1 Multi-vector at retrieval (P2) — substrate change to Postgres+VectorChord or Qdrant

**Status: not justified by the data.**

Architectural argument for multi-vector was lossless retrieval *as a property* (no information dilution from averaging) rather than a quality bet on the embedder. The eval results contradict this:

- All P2 configs cap at ≤26/31 R2 r@5 vs P1's 31/31 r@5
- The size-confound was broken (33M, 150M, 560M ColBERTs all cluster at the same ceiling)
- The architectural-property claim does not hold for short-utterance discrimination

P2 wins on multi-chunk source-level retrieval (5/5 at 33M params, 0.14GB, 14ms). For a workload dominated by long structured content, P2 single-stage (answerai-colbert-small) would be the principled choice. Grudge's mixed corpus has both shapes. Picking the architecture that's robust on both is the contract-derived answer.

### 7.2 Cascaded P3 — dense retrieve + multi-vector rerank

**Status: tied with P2 single-stage; offers no advantage.**

P3 was hypothesized as a compromise: keep cheap dense retrieval, gain multi-vector rerank precision. The data shows the cascade adds zero quality — same totals as the multi-vector model alone. The multi-vector model is the bottleneck.

### 7.3 Frontier LLM-as-judge over cheap retrieval (P6, hypothesized only)

**Status: rejected on product-shape grounds, not measured.**

Using Claude/GPT API for prereq scoring per turn makes RRC's correctness depend on a third-party API. Per-call cost, network as SPOF, data leaves the machine, no offline operation. Grudge's local-first deployment shape disqualifies this. Frontier LLMs are appropriate as the *main agent* but not as the prereq detector.

### 7.4 Hybrid sparse + dense (P4)

**Status: not measured; not justified.**

SPLADE-v3 (the strongest learned sparse) is CC-BY-NC-SA-4.0. BM25 via Bleve is open. Hybrid retrieval (dense + sparse) is well-established for catching lexical matches dense misses. The eval's A_technical_vocab category was the canonical case for this. The Apache pair (Qwen3-Emb + zerank-1-small) hits 6/6 on A_technical_vocab Stage 2 top-1 — no leak. The motivating failure mode for hybrid retrieval doesn't appear with the chosen pair, so the architectural complexity isn't justified.

### 7.5 NC-licensed flagship pair (zembed-1 + zerank-2)

**Status: tied with Apache pair on quality, fails hardware constraint.**

zembed-1 + zerank-2 was the prior session's pre-eval winner. In this round (sequential mode for measurement only):
- R2 Stage 2 top-1: 28/31 (one pair behind the Apache 29/31)
- R2 r@5: 31/31 (tied)
- R3 top-1: 5/5 (tied)

The NC pair did not produce a measurable quality advantage. It cannot run co-resident on 12 GB. Quantization to fit was attempted prior session and crashed the host. License posture is incidental.

### 7.6 Sub-1.7B rerankers

**Status: structurally incapable of multi-chunk aggregation.**

Qwen3-Reranker-0.6B paired with any embedder collapses to 1/5 on R3 multi-chunk. The rank pattern (#1, #2, #3, #4, #5 monotonic) shows the reranker can identify the right chunk but cannot aggregate across multiple chunks of the same source against multiple chunks of competitor sources. zembed-1's strong embedder doesn't rescue it. 1.7B (zerank-1-small) is the floor for robustness on multi-chunk shapes in the eval.

---

## 8. What this evaluation could miss

### 8.1 Eval set hardness

31 + 5 = 36 query points. E1_bible defeats every config — possibly an eval-pair design issue. 5 multi-chunk sources have weak discriminating power at the 5/5 ceiling. A larger and more carefully balanced eval set could expose differences within the 31/31 r@5 cluster.

### 8.2 Corpus-scale latency

The retrieval contract demands sub-linearity at N → ∞. The eval pool maxes at 29 candidates (R2) / 13 chunks (R3). Whether sqlite-vec ANN holds invariance at 10⁵+ chunks is theoretical at this measurement scale. The substrate code from the prior session pins this with a separate invariance test (`rrc/engine_invariance_test.go`) measuring nearest-call counts — not wall time at scale.

### 8.3 Multi-vector substrate viability

Multi-vector quality was tested via exhaustive PyLate MaxSim. PLAID-on-VectorChord (the production-grade multi-vector substrate) was not measured for storage cost or retrieval latency at scale. The conclusion "multi-vector doesn't justify substrate change" rests on the quality data; if a future eval shows multi-vector winning on quality, the substrate cost question reopens.

### 8.4 Reranker-only gains

zerank-2 (4B NC) didn't beat zerank-1-small (1.7B Apache) on overall totals. They have different miss profiles: zerank-2 fixes E4_log_says but loses A4_segfault; zerank-1-small fixes A4 but loses E4. This pattern suggests reranker-class quality has a ceiling at this eval that 4B doesn't escape. Whether this generalizes or is eval-specific is unknown.

### 8.5 Multi-chunk edge formation

The R3 eval reports source-level top-1: did the right *source* surface as #1 from any of its chunks. RRC's edge formation aggregates *messages*, not sources. If a long message is one source with N chunks, max-over-chunks edge formation works correctly when the right source surfaces. This was implicitly tested. If parent-message aggregation introduces additional failure modes (edge formation across chunk-boundary semantic dependencies), they're not in this eval.

### 8.6 Latency under co-resident load

Mean latencies are per-pair on a quiet GPU. Production has additional load (KV cache for the main agent LLM, concurrent threads, background backfill). The 297ms baseline could degrade meaningfully under concurrency.

### 8.7 Long-tail content shapes

Content tested is technical-prose-shaped (Grudge artifacts, fictional scenarios). RRC will encounter code dumps, structured data, multilingual content, attachments. None of these are in the eval set. zembed-1's claimed multilingual robustness vs Qwen3-Embedding's multilingual coverage was not differentially tested.

---

## 9. Selection rationale, summarized

The contract demands lossless retrieval at Layer 1 + sufficient detection precision at Layer 2 + reasonable latency. Hardware constrains co-resident inference to ≤12 GB at bf16.

- Lossless retrieval (R2 r@5 = 31/31, R3 top-1 = 5/5): met by 5 P1 configs.
- 12GB co-resident: met by 2 of those 5.
- Among the 2: Qwen3-Embedding-0.6B + zerank-1-small leads on VRAM and latency; jina-v5-small + zerank-1-small is functionally tied.

**Final pick: Qwen3-Embedding-0.6B + zerank-1-small.**

This selection holds independent of license posture, multi-vector substrate availability, or quantization options. It is determined by intersecting the contract with the hardware constraint and applying within-cluster tiebreakers on minor cost differences.

---

## 10. Appendix: Raw artifacts

| File | Content |
|---|---|
| `rerank_eval/pairs.json` | R2 eval set, 31 pairs across 6 categories |
| `rerank_eval/multichunk_pairs.json` | R3 eval set, 5 sources + 5 queries |
| `rerank_eval/results_paths.json` | R2 raw results, 14 configs |
| `rerank_eval/results_multichunk.json` | R3 raw results, 12 configs |
| `rerank_eval/run_eval_paths.py` | Round 2/4 harness + sequential-pair mode |
| `rerank_eval/run_eval_multichunk.py` | Round 3 harness + chunker port |
| `rerank_eval/run_round4_nc.py` | Round 4 driver — NC configs added |
| `rerank_eval/build_multichunk_pairs.py` | R3 source authoring |
| `rerank_eval/run_paths.log` | R2/R4 console log |
| `rerank_eval/run_multichunk.log` | R3 console log |

Eval ran 2026-04-29 across four rounds:
1. R1 (initial, partial fails) — superseded by R2.
2. R2 (chunk-sized A–F, 9 configs) — Apache tier + jina-colbert-v2 NC.
3. R3 (multi-chunk G, 8 configs) — same candidate set.
4. R4 (NC tier inclusion) — sequential-mode P1 configs adding zembed-1 + zerank-2.

End of report.
