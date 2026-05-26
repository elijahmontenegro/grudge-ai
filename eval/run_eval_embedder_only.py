"""Embedder-only path eval: bi-encoder retrieval, no reranker. Cosine score
becomes the edge score directly.

Tests whether qwen3-emb-0.6b alone satisfies RRC's recall contract on:
  - R2 (31-pair Stage 2, 29-way pool with F_long_document included)
  - R3 (5-source multi-chunk, max-over-chunks aggregation)

If both clear (R2 r@5 = 31/31, R3 top-1 = 5/5), there's a real ~1.5GB tier
between Tier S (chosen pair, 4.64GB) and Tier C (answerai-colbert, 0.14GB
but lossy). If either misses, the cliff is real and unbridgeable without
a Layer 2.

Re-uses run_eval_paths.py loaders so the embedder + prompt formatting is
identical to the existing P1 results — head-to-head comparable.
"""
from __future__ import annotations

import json
import time
from collections import defaultdict
from pathlib import Path

import numpy as np

import run_eval_paths as r2_harness
import run_eval_multichunk as r3_harness


PAIRS_FILE_R2 = Path(__file__).parent / "pairs.json"
PAIRS_FILE_R3 = Path(__file__).parent / "multichunk_pairs.json"
OUT_FILE = Path(__file__).parent / "results_embedder_only.json"


def eval_r2_embedder_only(loader, name):
    """R2 Stage 1 (5-way) and Stage 2 (29-way) — cosine ranking only, no reranker."""
    print(f"\n=== R2 embedder-only: {name} ===", flush=True)
    r2_harness.cleanup()
    r2_harness.reset_gpu_stats()

    print(f"  loading {name} ...", flush=True)
    t0 = time.time()
    model, embed_q, embed_p = loader()
    load_s = time.time() - t0
    peak = r2_harness.gpu_peak_gb()
    print(f"    loaded in {load_s:.1f}s, peak VRAM={peak:.2f}GB", flush=True)

    print(f"  encoding {len(r2_harness.ALL_CORRECTS)} corrects ...", flush=True)
    correct_embs = embed_p(r2_harness.ALL_CORRECTS)

    results = {
        "stage1": defaultdict(list),
        "stage2": defaultdict(list),
        "co_resident_peak_gb": peak,
        "load_s": load_s,
    }

    for p in r2_harness.all_pairs:
        cat = p["cat"]; qid = p["id"]; ci = r2_harness.CORRECT_IDX[qid]

        # Stage 1: 5-way (correct + 4 distractors), pure cosine
        t0 = time.time()
        q_emb = embed_q([p["query"]])[0]
        d_embs = embed_p(p["distractors"])
        c_emb = correct_embs[ci]
        s1_embs = np.vstack([c_emb[None, :], d_embs])
        s1_scores = s1_embs @ q_emb
        s1_rank = sorted(range(len(s1_scores)), key=lambda i: -s1_scores[i]).index(0) + 1
        s1_ms = (time.time() - t0) * 1000

        # Stage 2: 29-way pool (correct + 4 distractors + 24 other corrects), pure cosine
        t0 = time.time()
        other_idx = [i for i in range(len(r2_harness.ALL_CORRECTS)) if i != ci]
        other_embs = correct_embs[other_idx]
        s2_embs = np.vstack([c_emb[None, :], d_embs, other_embs])
        s2_scores = s2_embs @ q_emb
        s2_rank = sorted(range(len(s2_scores)), key=lambda i: -s2_scores[i]).index(0) + 1
        s2_ms = (time.time() - t0) * 1000

        results["stage1"][cat].append((qid, s1_rank, s1_ms))
        results["stage2"][cat].append((qid, s2_rank, s2_ms))
        print(f"    {cat[:3]} {qid:18s} S1=#{s1_rank}/5  S2=#{s2_rank}/29  s1={s1_ms:.0f}ms s2={s2_ms:.0f}ms",
              flush=True)

    del model, embed_q, embed_p
    r2_harness.cleanup()
    return results


def eval_r3_embedder_only(loader, name, sources, pairs):
    """R3 multi-chunk — chunk every source, rank chunks by cosine, aggregate to
    source via max-over-chunks (same shape as production engine's edge formation)."""
    print(f"\n=== R3 embedder-only: {name} ===", flush=True)
    r2_harness.cleanup()
    r2_harness.reset_gpu_stats()

    print(f"  loading {name} ...", flush=True)
    t0 = time.time()
    model, embed_q, embed_p = loader()
    load_s = time.time() - t0
    peak = r2_harness.gpu_peak_gb()
    print(f"    loaded in {load_s:.1f}s, peak VRAM={peak:.2f}GB", flush=True)

    chunk_texts = []
    chunk_to_source = []
    for sid, text in sources.items():
        for c in r3_harness.chunk_text(text):
            chunk_texts.append(c)
            chunk_to_source.append(sid)
    print(f"  total chunks across {len(sources)} sources: {len(chunk_texts)}", flush=True)

    print(f"  encoding chunks ...", flush=True)
    chunk_embs = embed_p(chunk_texts)

    results = {"per_pair": [], "co_resident_peak_gb": peak, "load_s": load_s,
               "total_chunks": len(chunk_texts)}

    for p in pairs:
        qid = p["id"]; correct_sid = p["correct_source_id"]
        t0 = time.time()
        q_emb = embed_q([p["query"]])[0]
        chunk_scores = (chunk_embs @ q_emb).tolist()
        ranking = r3_harness.rank_sources(chunk_scores, chunk_to_source)
        rank = ranking.index(correct_sid) + 1
        ms = (time.time() - t0) * 1000
        results["per_pair"].append((qid, correct_sid, rank, ms))
        print(f"    {qid:25s} correct={correct_sid:30s} source-rank=#{rank}/{len(sources)}  {ms:.0f}ms",
              flush=True)

    del model, embed_q, embed_p
    r2_harness.cleanup()
    return results


def main():
    # R3 inputs
    r3_data = json.loads(PAIRS_FILE_R3.read_text(encoding="utf-8"))
    sources = r3_data["sources"]
    r3_pairs = r3_data["pairs"]

    configs = [
        ("p0_qwen3-emb-0.6b_only", r2_harness.load_dense_qwen3_emb_06b, "Qwen3-Embedding-0.6B"),
        ("p0_zembed-1_only", r2_harness.load_dense_zembed_1, "zembed-1 (4B, NC)"),
    ]

    all_results = {}
    for label, loader, display_name in configs:
        try:
            r2_res = eval_r2_embedder_only(loader, display_name)
            r3_res = eval_r3_embedder_only(loader, display_name, sources, r3_pairs)
            all_results[label] = {"path": "P0", "r2": r2_res, "r3": r3_res}
        except Exception as e:
            print(f"\n!!! {label} FAILED: {type(e).__name__}: {e}", flush=True)
            all_results[label] = {"path": "P0", "error": f"{type(e).__name__}: {e}"}
        r2_harness.cleanup()

    # Summary
    print("\n\n" + "=" * 100)
    print("=== EMBEDDER-ONLY SUMMARY ===")
    print("=" * 100)
    cats = sorted({c for res in all_results.values() if "r2" in res
                   for c in res["r2"]["stage2"].keys()})
    for label, res in all_results.items():
        if "error" in res:
            print(f"{label}: FAILED — {res['error']}")
            continue
        r2 = res["r2"]; r3 = res["r3"]
        # R2 Stage 2 totals
        s2_top1 = s2_r5 = s2_r10 = total = 0
        for c in cats:
            for _, r, _ in r2["stage2"].get(c, []):
                total += 1
                if r <= 1: s2_top1 += 1
                if r <= 5: s2_r5 += 1
                if r <= 10: s2_r10 += 1
        # R3
        r3_t1 = sum(1 for x in r3["per_pair"] if x[2] <= 1)
        r3_n = len(r3["per_pair"])
        peak = r2["co_resident_peak_gb"]
        print(f"\n{label}  ({peak:.2f}GB peak)")
        print(f"  R2 Stage 2: top1={s2_top1}/{total}  r@5={s2_r5}/{total}  r@10={s2_r10}/{total}")
        print(f"  R3 source: top1={r3_t1}/{r3_n}")

    # Serialize (defaultdicts → dicts)
    serializable = {}
    for label, res in all_results.items():
        if "error" in res:
            serializable[label] = res; continue
        r2 = res["r2"]
        r2_clean = {k: v for k, v in r2.items() if not isinstance(v, defaultdict)}
        r2_clean["stage1"] = {c: list(rows) for c, rows in r2["stage1"].items()}
        r2_clean["stage2"] = {c: list(rows) for c, rows in r2["stage2"].items()}
        serializable[label] = {"path": res["path"], "r2": r2_clean, "r3": res["r3"]}
    OUT_FILE.write_text(json.dumps(serializable, indent=2), encoding="utf-8")
    print(f"\nraw -> {OUT_FILE}")


if __name__ == "__main__":
    main()
