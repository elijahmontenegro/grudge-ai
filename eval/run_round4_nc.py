"""Round 4 — license filter dropped. Adds zembed-1 (4B, NC) and zerank-2 (4B, NC)
to the candidate set. Uses sequential P1 mode where the pair exceeds 12GB
co-resident (zembed-1 + zerank-2 = ~16GB).

Re-runs round 2 (chunk-sized A-F) and round 3 (multi-chunk G) on the new
configs only; the existing Apache-tier results in results_paths.json and
results_multichunk.json stand."""
from __future__ import annotations

import json
import time
from pathlib import Path

import torch

import run_eval_paths as harness
import run_eval_multichunk as mc_harness


OUT_R2 = Path(__file__).parent / "results_paths.json"
OUT_R3 = Path(__file__).parent / "results_multichunk.json"


# Round 2 (chunk-sized A-F) — sequential P1 configs that don't fit co-resident.
# All four involve at least one 4B NC model.
R2_CONFIGS = [
    ("p1_zembed-1+zerank-2", "P1",
     lambda: harness.eval_path_p1_sequential(
         harness.load_dense_zembed_1, harness.load_rerank_zerank_2,
         "zembed-1 (4B, NC)", "zerank-2 (4B, NC)")),
    ("p1_zembed-1+zerank-1-small", "P1",
     lambda: harness.eval_path_p1_sequential(
         harness.load_dense_zembed_1, harness.load_rerank_zerank_1_small,
         "zembed-1 (4B, NC)", "zerank-1-small (1.7B, Apache)")),
    ("p1_zembed-1+qwen3-rerank-0.6b", "P1",
     lambda: harness.eval_path_p1_sequential(
         harness.load_dense_zembed_1, harness.load_rerank_qwen3_06b,
         "zembed-1 (4B, NC)", "Qwen3-Reranker-0.6B (Apache)")),
    ("p1_qwen3-emb-0.6b+zerank-2", "P1",
     lambda: harness.eval_path_p1_sequential(
         harness.load_dense_qwen3_emb_06b, harness.load_rerank_zerank_2,
         "Qwen3-Embedding-0.6B (Apache)", "zerank-2 (4B, NC)")),
]


# Round 3 (multi-chunk G) — same configs adapted to multi-chunk pipeline.
# We need a sequential variant of eval_p1 in the multichunk harness too.
def eval_p1_multichunk_sequential(layer1_loader, layer2_loader, l1_name, l2_name, sources, pairs):
    print(f"\n=== P1 (sequential, multichunk): {l1_name} + {l2_name} ===", flush=True)
    harness.cleanup()
    harness.reset_gpu_stats()

    # ---- Layer 1 pass ----
    print(f"  loading L1={l1_name} ...", flush=True)
    t0 = time.time()
    l1_model, embed_q, embed_p = layer1_loader()
    l1_load_s = time.time() - t0
    print(f"    L1 loaded in {l1_load_s:.1f}s, VRAM={harness.gpu_mem_gb():.2f}GB", flush=True)

    chunk_texts: list[str] = []
    chunk_to_source: list[str] = []
    for sid, text in sources.items():
        for c in mc_harness.chunk_text(text):
            chunk_texts.append(c)
            chunk_to_source.append(sid)

    print(f"  total chunks: {len(chunk_texts)}", flush=True)
    print(f"  encoding chunks (dense) ...", flush=True)
    chunk_embs = embed_p(chunk_texts)

    # Cache per-pair Layer 1 scores
    per_pair_l1 = {}
    for p in pairs:
        qid = p["id"]
        q_emb = embed_q([p["query"]])[0]
        scores = (chunk_embs @ q_emb).tolist()
        per_pair_l1[qid] = scores

    l1_peak = harness.gpu_peak_gb()
    del l1_model, embed_q, embed_p, chunk_embs
    harness.cleanup()
    harness.reset_gpu_stats()

    # ---- Layer 2 pass ----
    print(f"  loading L2={l2_name} ...", flush=True)
    t0 = time.time()
    l2_model, score_fn = layer2_loader()
    l2_load_s = time.time() - t0
    l2_peak = harness.gpu_peak_gb()
    print(f"    L2 loaded in {l2_load_s:.1f}s, peak={l2_peak:.2f}GB", flush=True)

    results = {"per_pair": [],
               "co_resident_peak_gb": max(l1_peak, l2_peak),
               "l1_peak_gb": l1_peak, "l2_peak_gb": l2_peak,
               "sequential": True,
               "l1_load_s": l1_load_s, "l2_load_s": l2_load_s,
               "total_chunks": len(chunk_texts)}

    for p in pairs:
        qid = p["id"]; correct_sid = p["correct_source_id"]
        l1_scores = per_pair_l1[qid]
        topk_idx = sorted(range(len(chunk_texts)), key=lambda i: -l1_scores[i])[:mc_harness.RERANK_TOP_K]
        topk_texts = [chunk_texts[i] for i in topk_idx]
        t0 = time.time()
        l2_scores = score_fn(p["query"], topk_texts)
        ms = (time.time() - t0) * 1000
        full_scores = [-1e9] * len(chunk_texts)
        for idx, s in zip(topk_idx, l2_scores):
            full_scores[idx] = s
        ranking = mc_harness.rank_sources(full_scores, chunk_to_source)
        rank = ranking.index(correct_sid) + 1
        results["per_pair"].append((qid, correct_sid, rank, ms))
        print(f"    {qid:25s} correct={correct_sid:30s} source-rank=#{rank}/{len(sources)}  {ms:.0f}ms",
              flush=True)

    del l2_model, score_fn
    harness.cleanup()
    return results


# Multichunk pairs file
mc_data = json.loads((Path(__file__).parent / "multichunk_pairs.json").read_text(encoding="utf-8"))
mc_sources = mc_data["sources"]
mc_pairs = mc_data["pairs"]

R3_CONFIGS = [
    ("p1_zembed-1+zerank-2", "P1",
     lambda: eval_p1_multichunk_sequential(
         harness.load_dense_zembed_1, harness.load_rerank_zerank_2,
         "zembed-1 (4B, NC)", "zerank-2 (4B, NC)", mc_sources, mc_pairs)),
    ("p1_zembed-1+zerank-1-small", "P1",
     lambda: eval_p1_multichunk_sequential(
         harness.load_dense_zembed_1, harness.load_rerank_zerank_1_small,
         "zembed-1 (4B, NC)", "zerank-1-small (1.7B, Apache)", mc_sources, mc_pairs)),
    ("p1_zembed-1+qwen3-rerank-0.6b", "P1",
     lambda: eval_p1_multichunk_sequential(
         harness.load_dense_zembed_1, harness.load_rerank_qwen3_06b,
         "zembed-1 (4B, NC)", "Qwen3-Reranker-0.6B (Apache)", mc_sources, mc_pairs)),
    ("p1_qwen3-emb-0.6b+zerank-2", "P1",
     lambda: eval_p1_multichunk_sequential(
         harness.load_dense_qwen3_emb_06b, harness.load_rerank_zerank_2,
         "Qwen3-Embedding-0.6B (Apache)", "zerank-2 (4B, NC)", mc_sources, mc_pairs)),
]


def merge_into(out_path: Path, new_results: dict):
    if out_path.exists():
        existing = json.loads(out_path.read_text(encoding="utf-8"))
    else:
        existing = {}
    existing.update(new_results)
    out_path.write_text(json.dumps(existing, indent=2), encoding="utf-8")


def serialize_r2(res):
    """Match the format from run_eval_paths.py main() so merging works."""
    if "error" in res:
        return res
    clean = {"path": res.get("path", "?")}
    for k in ("co_resident_peak_gb", "l1_peak_gb", "l2_peak_gb",
             "sequential", "l1_load_s", "l2_load_s"):
        if k in res:
            clean[k] = res[k]
    clean["stage1"] = {c: list(rows) for c, rows in res["stage1"].items()}
    clean["stage2"] = {c: list(rows) for c, rows in res["stage2"].items()}
    return clean


def main():
    print("=" * 80)
    print("Round 4 — Round 2 shape (chunk-sized A-F, sequential P1)")
    print("=" * 80)
    r2_results = {}
    for label, path_name, runner in R2_CONFIGS:
        try:
            res = runner()
            r2_results[label] = serialize_r2({"path": path_name, **res})
        except Exception as e:
            print(f"\n!!! {label} FAILED: {type(e).__name__}: {e}", flush=True)
            r2_results[label] = {"path": path_name, "error": f"{type(e).__name__}: {e}"}
        harness.cleanup()
    merge_into(OUT_R2, r2_results)
    print(f"\nround-2 NC results merged -> {OUT_R2}")

    print("\n" + "=" * 80)
    print("Round 4 — Round 3 shape (multi-chunk G, sequential P1)")
    print("=" * 80)
    r3_results = {}
    for label, path_name, runner in R3_CONFIGS:
        try:
            res = runner()
            r3_results[label] = {"path": path_name, **res}
        except Exception as e:
            print(f"\n!!! {label} FAILED: {type(e).__name__}: {e}", flush=True)
            r3_results[label] = {"path": path_name, "error": f"{type(e).__name__}: {e}"}
        harness.cleanup()
    merge_into(OUT_R3, r3_results)
    print(f"\nround-3 NC results merged -> {OUT_R3}")

    print("\nDone.")


if __name__ == "__main__":
    main()
