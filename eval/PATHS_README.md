# Multi-path eval: P1 vs P2 vs P3

This eval measures the three architectural paths that satisfy RRC's contract (lossless
prereq retrieval + sub-linear per-step compute), against extended `pairs.json` that now
includes `F_long_document` — the failure mode that distinguishes single-vector from
multi-vector representations.

## Paths

- **P1 — Single dense + dedicated reranker.** Today's shape. Lossless retrieval is a
  *quality bet* on the embedder model.
- **P2 — Multi-vector ColBERT alone (single-stage).** Lossless retrieval as
  *architectural property* (no information dilution from averaging). One model only.
- **P3 — Dense retrieve + multi-vector rerank live-encoded.** Hybrid. Cheap retrieval
  substrate (today's), multi-vector quality at rerank.

## Candidates (all Apache 2.0)

P1 (Layer 1 + Layer 2):
- `Qwen3-Embedding-0.6B` (1.2GB) + `Qwen3-Reranker-0.6B` (1.2GB) = ~2.4GB
- `Qwen3-Embedding-0.6B` + `zerank-1-small` (1.7B → 3.4GB) = ~4.6GB
- `jina-v5-text-small` (1.2GB) + `zerank-1-small` = ~4.6GB

P2 (single model):
- `answerai-colbert-small-v1` (33M, ~70MB) — English
- `SauerkrautLM-Multi-ModernColBERT` (~150M) — multilingual

P3 (Layer 1 dense + multi-vector rerank):
- `Qwen3-Embedding-0.6B` + `answerai-colbert-small-v1` = ~1.27GB
- `jina-v5-small` + `answerai-colbert-small-v1` = ~1.27GB

All combinations fit ≤10GB on a 12GB GPU with headroom for KV cache and runtime overhead.

## Run

```
cd rerank_eval/

# One-time deps
pip install torch sentence-transformers transformers numpy pylate accelerate

# Run
python run_eval_paths.py
```

Output: `results_paths.json` + console summary table.

## What the eval measures

For each (path, candidate combo):

1. **Stage 1 top-1** per category, including the new `F_long_document`. Hit if the
   correct candidate ranks #1 in the 5-way (correct + 4 distractors) competition.
2. **Stage 2 recall@K** (K=1, 5, 10) over the cross-pollinated pool of 31 candidates
   (correct + 4 distractors + 26 other-pair corrects across all categories — long and
   short are mixed).
3. **Co-resident peak VRAM**. Both Layer 1 and Layer 2 loaded simultaneously, peak
   measured under inference. This is the deployment-shape test the prior eval missed
   (zembed-1 + zerank-2 was selected on per-model quality, the pair didn't fit).
4. **Per-turn latency** for stages 1 and 2 (mean ms per pair).

## Reading the output

The decision criteria, in priority order against RRC's contract:

1. **`F_long_document` Stage 2 recall@10** — does the candidate find the right long doc
   when the corpus is full of similar-length distractors? **This is the lossless-retrieval
   test for paths claiming the property.** A candidate that misses ≥1 of 6 here is not
   structurally lossless on long-doc workloads.
2. **Overall Stage 1 top-1** — does the path produce edges only on real prereqs (precision
   at the detection layer)? Threshold is whatever survives the eval as the field winner.
3. **Co-resident peak VRAM ≤ 10GB** — fits on the 12GB target with KV-cache headroom.
   Hard cut.
4. **Mean S1 latency** — ≤500ms is the comfortable target for chat-feel turns;
   ≤2000ms is acceptable for "thinking" feel. Multi-vector P2 should win here (no rerank
   stage); P1 with a 1.7B reranker may cross the 500ms line.

## Interpretation guide

- If P2 with `answerai-colbert-small-v1` achieves lossless on `F_long_document` AND
  ≥90% Stage 1 top-1 across all categories AND fits comfortably: **P2 is the answer**.
  Single model, single stage, structurally lossless.
- If P1 with the strongest pair achieves lossless on `F_long_document`: **the
  quality-bet validates** and P1 stays as the principled answer (single-binary
  preserved, simpler stack). The lossless property is empirical-not-architectural,
  but it holds.
- If P3 wins on a combination of Stage 1 precision and `F_long_document` recall: **P3
  is the principled middle**. Substrate stays, multi-vector quality at rerank.
- If nothing achieves lossless on `F_long_document`: **the eval set or the candidates
  need expansion** — current candidates aren't enough.

## Files

- `pairs.json` — eval set with categories A-F (F_long_document added in this round).
- `run_eval_paths.py` — the harness.
- `results_paths.json` — output of the run (per-pair ranks + VRAM + latency per config).
- `results_v2.json` — prior reranker eval (not directly comparable; different shape).
- `results_embed_v2.json` — prior embedder eval.

## Notes / caveats

- `pylate` is required for the multi-vector paths. Install if missing.
- ColBERT models truncate documents at ~512 tokens. The F_long_document corrects are
  ~2000-3500 chars (~500-900 tokens) so most of each doc is seen; tail truncation is
  acceptable since the buried answer is typically mid-document.
- Sequential model loading is unavoidable for P1 / P3 (load L1 → encode → load L2),
  but the *measurement* of co-resident peak happens after both are loaded, before
  Layer 2 inference begins. That's the relevant deployment-shape number.
- The eval doesn't yet test corpus-scale retrieval (1000+ chunks). Stage 2's 31-candidate
  pool is a proxy. If results favor multi-vector paths, the next round should add a
  larger corpus stress test before committing the substrate change.
