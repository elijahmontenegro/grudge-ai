# Substrate evaluation

This is the eval that produced the substrate decision recorded in
[`../docs/eval-reports/`](../docs/eval-reports/) — the choice of
`Qwen3-Embedding-0.6B` (TEI) + `zerank-1-small` (vLLM) as the production
pair on a 12 GB GPU.

## What's measured

Three architectural paths that satisfy RRC's contract (lossless prerequisite
retrieval + sub-linear per-step compute) across an extended seed set
(`rrc/calibrate/seed/pairs.json`)
including the `F_long_document` failure mode that distinguishes single-
vector from multi-vector representations:

- **P1** — Single dense + dedicated reranker. Lossless retrieval as a
  *quality bet* on the embedder.
- **P2** — Multi-vector ColBERT alone. Lossless as an *architectural
  property* (no information dilution from averaging).
- **P3** — Dense retrieve + multi-vector rerank live-encoded.

All candidates are Apache 2.0. The eval also enforces a *co-resident* VRAM
test: both stages loaded simultaneously under inference. This is what the
prior eval missed (zembed-1 + zerank-2 was a per-model winner that didn't
fit as a pair).

For each (path, candidate combo) it measures:

1. **Stage 1 top-1** per category — does the candidate produce edges only
   on real prereqs?
2. **Stage 2 recall@{1,5,10}** over the cross-pollinated 31-candidate pool.
3. **Co-resident peak VRAM** under simultaneous load.
4. **Per-turn latency** for stages 1 and 2 (mean ms per pair).

## Reproduce

```bash
# One-time
pip install torch sentence-transformers transformers numpy pylate accelerate

# Run
cd eval/
python run_eval.py
```

Output: `results_paths.json` (gitignored) plus a console summary table.

## Decision criteria (priority order)

1. `F_long_document` Stage 2 recall@10 — the lossless-retrieval test for
   paths that claim the property. Missing ≥1 of 6 rules out the path on
   long-doc workloads.
2. Overall Stage 1 top-1 — precision at the detection layer.
3. Co-resident peak VRAM ≤ 10 GB — fits on the 12 GB target with
   KV-cache headroom.
4. Mean Stage 1 latency — ≤500 ms is chat-feel, ≤2000 ms is acceptable for
   thinking-feel.

## Files

- `../rrc/calibrate/seed/pairs.json` — the canonical seed/eval set, categories A–F
  (including `F_long_document`). Lives with the library so self-calibration ships
  embedded; this harness reads it by path.
- `run_eval.py` — the harness for the multi-path comparison.
- `run_live_smoke.py` — production validator. Hits the running TEI + vLLM
  substrate and compares against the eval baseline (R2 r@5 = 31/31,
  lossless contract). Invoked by `task substrate:smoke`.

`results_*.json` and `run_*.log` are gitignored — they're run artifacts,
not source.
