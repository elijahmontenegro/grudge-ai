"""Run the 25-pair discrimination test against multiple cross-encoder rerankers
loaded sequentially via sentence-transformers (uniform code path, frees GPU
between models).

Models tested (~568M to ~4B params, all cross-encoders):
  - BAAI/bge-reranker-v2-m3 (baseline, current production)
  - mixedbread-ai/mxbai-rerank-large-v2
  - Qwen/Qwen3-Reranker-4B
  - zeroentropy/zerank-2

Output: per-model overall top-1 hits, per-category breakdown, mean z-score,
mean per-pair latency.
"""
import json, math, time, gc, sys
from collections import defaultdict
from pathlib import Path

import torch
from sentence_transformers import CrossEncoder

PAIRS_FILE = Path(__file__).parent / "pairs.json"
with PAIRS_FILE.open() as f:
    data = json.load(f)

MODELS = [
    ("bge-m3",   "BAAI/bge-reranker-v2-m3"),
    ("mxbai-v2", "mixedbread-ai/mxbai-rerank-large-v2"),
    ("qwen3-4b", "Qwen/Qwen3-Reranker-4B"),
    ("zerank-2", "zeroentropy/zerank-2"),
]

def zscore(v, arr):
    n = len(arr)
    m = sum(arr) / n
    var = sum((x - m) ** 2 for x in arr) / n
    sd = math.sqrt(var) if var > 0 else 1e-12
    return (v - m) / sd

# results[model_short] = {category: [(qid, rank_correct, z_correct, latency_ms)]}
results = {short: defaultdict(list) for short, _ in MODELS}

for short, hf_id in MODELS:
    print(f"\n=== {short} ({hf_id}) ===", flush=True)
    print("loading...", flush=True)
    t0 = time.time()
    try:
        model = CrossEncoder(hf_id, trust_remote_code=True, device="cuda", model_kwargs={"torch_dtype": torch.bfloat16})
    except Exception as e:
        print(f"LOAD FAILED for {short}: {e}", flush=True)
        continue
    print(f"loaded in {time.time()-t0:.1f}s, mem={torch.cuda.memory_allocated()/1e9:.2f}GB", flush=True)

    for cat, pairs in data["categories"].items():
        for p in pairs:
            cands = [p["correct"]] + p["distractors"]
            query_doc_pairs = [(p["query"], c) for c in cands]
            t0 = time.time()
            try:
                scores = model.predict(query_doc_pairs, show_progress_bar=False)
            except Exception as e:
                print(f"  {cat[:3]} {p['id']}: predict failed: {e}", flush=True)
                results[short][cat].append((p["id"], None, None, None))
                continue
            scores = list(scores) if hasattr(scores, "__iter__") else [scores]
            # ensure list of floats
            scores = [float(s) for s in scores]
            ranking = sorted(range(len(cands)), key=lambda i: -scores[i])
            rank_correct = ranking.index(0) + 1
            z = zscore(scores[0], scores)
            latency_ms = (time.time() - t0) * 1000
            top1 = cands[ranking[0]][:32]
            results[short][cat].append((p["id"], rank_correct, z, latency_ms))
            print(f"  {cat[:3]} {p['id']:18s} rank={rank_correct} z={z:+.2f} {latency_ms:5.0f}ms  top1={top1}", flush=True)

    # free GPU
    del model
    gc.collect()
    torch.cuda.empty_cache()
    torch.cuda.synchronize()
    print(f"unloaded, mem={torch.cuda.memory_allocated()/1e9:.2f}GB", flush=True)

# Summary
print("\n\n=== Summary: top-1 hit rate per category ===", flush=True)
cats = sorted(data["categories"].keys())
print(f"{'model':10s}  " + "  ".join(f"{c[:14]:>14s}" for c in cats) + f"  {'OVERALL':>10s}  {'mean_z':>7s}  {'mean_ms':>8s}")
for short, _ in MODELS:
    row_hits = []
    total_hits = total = 0
    all_z = []
    all_ms = []
    for c in cats:
        rows = results[short].get(c, [])
        n = len(rows)
        hits = sum(1 for _, r, _, _ in rows if r == 1)
        total_hits += hits
        total += n
        row_hits.append(f"{hits}/{n}")
        for _, _, z, ms in rows:
            if z is not None:
                all_z.append(z)
            if ms is not None:
                all_ms.append(ms)
    mz = sum(all_z)/len(all_z) if all_z else 0
    mms = sum(all_ms)/len(all_ms) if all_ms else 0
    cell_str = "  ".join(f"{rh:>14s}" for rh in row_hits)
    print(f"{short:10s}  {cell_str}  {total_hits}/{total:<7d}  {mz:+7.2f}  {mms:>7.0f}ms")

# Save raw results
out_path = Path(__file__).parent / "results.json"
with out_path.open("w") as f:
    json.dump({m: {c: list(rows) for c, rows in cats_dict.items()} for m, cats_dict in results.items()}, f, indent=2)
print(f"\nraw results -> {out_path}")
