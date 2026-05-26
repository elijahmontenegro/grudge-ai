"""Evaluate four scoring models on a categorized prerequisite-dependency test set.

Each query has 1 correct premise and 4 distractors. We score each candidate
against the query and rank by score. The "discrimination" question is whether
the correct candidate ranks #1.

Models tested:
  - bge-reranker-v2-m3 (current production, port 8080)
  - bge-reranker-large (port 8091)
  - mxbai-rerank-large-v1 (port 8092, single-pair due to TEI batching bug)
  - cross-encoder/nli-deberta-v3-base (port 8090, /predict, entailment class)

Output: per-model overall top-1 hits, per-category breakdown, mean z-score.
"""
import json, urllib.request, math, sys
from collections import defaultdict

PAIRS_FILE = "rerank_eval/pairs.json"

with open(PAIRS_FILE) as f:
    data = json.load(f)

def rerank_batched(url, query, candidates):
    body = json.dumps({"query": query, "texts": candidates, "truncate": True}).encode()
    req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"})
    resp = json.loads(urllib.request.urlopen(req).read())
    out = [0.0]*len(candidates)
    for h in resp:
        out[h["index"]] = h["score"]
    return out

def rerank_single(url, query, candidates):
    out = []
    for c in candidates:
        body = json.dumps({"query": query, "texts": [c], "truncate": True}).encode()
        req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"})
        resp = json.loads(urllib.request.urlopen(req).read())
        out.append(resp[0]["score"])
    return out

def nli_entailment(url, premises, hypothesis):
    """Score each (premise, hypothesis) for NLI entailment-class probability."""
    out = []
    for p in premises:
        body = json.dumps({"inputs": [[p, hypothesis]], "truncate": True, "raw_scores": False}).encode()
        req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"})
        resp = json.loads(urllib.request.urlopen(req).read())
        d = {h["label"].lower(): h["score"] for h in resp[0]}
        out.append(d.get("entailment", 0.0))
    return out

def zscore(v, arr):
    n = len(arr)
    m = sum(arr)/n
    var = sum((x-m)**2 for x in arr)/n
    sd = math.sqrt(var) if var > 0 else 1e-12
    return (v - m)/sd

models = [
    ("bge-m3",    lambda q, cs: rerank_batched("http://localhost:8080/rerank", q, cs)),
    ("bge-large", lambda q, cs: rerank_batched("http://localhost:8091/rerank", q, cs)),
    ("mxbai",     lambda q, cs: rerank_single ("http://localhost:8092/rerank", q, cs)),
    ("nli",       lambda q, cs: nli_entailment("http://localhost:8090/predict", cs, q)),
]

# results[model][category] = list of (qid, rank_correct, z_correct)
results = {m[0]: defaultdict(list) for m in models}

print(f"{'cat':4s} {'qid':18s} {'model':10s} {'rank':>4s} {'corr.score':>11s} {'corr.z':>7s}  top1")
print("-" * 110)
for cat, pairs in data["categories"].items():
    for p in pairs:
        cands = [p["correct"]] + p["distractors"]
        for name, fn in models:
            try:
                scores = fn(p["query"], cands)
                ranking = sorted(range(len(cands)), key=lambda i: -scores[i])
                rank_correct = ranking.index(0) + 1
                z = zscore(scores[0], scores)
                top1 = cands[ranking[0]][:32]
                results[name][cat].append((p["id"], rank_correct, z))
                print(f"{cat[:3]:4s} {p['id']:18s} {name:10s} {rank_correct:>4d} {scores[0]:>11.4f} {z:+7.2f}  {top1}")
            except Exception as e:
                print(f"{cat[:3]:4s} {p['id']:18s} {name:10s} ERROR: {e}")
                results[name][cat].append((p["id"], None, None))
        print()

# Per-category and overall summaries
print()
print("=== Per-category top-1 hit rate ===")
cats = sorted(data["categories"].keys())
header = "model     " + "  ".join(f"{c[:14]:>14s}" for c in cats) + f"  {'OVERALL':>10s}"
print(header)
for name, _ in models:
    row = f"{name:10s}"
    total_hits = total = 0
    for c in cats:
        rows = results[name][c]
        n = len(rows)
        hits = sum(1 for _, r, _ in rows if r == 1)
        total_hits += hits
        total += n
        row += f"  {hits}/{n:>3d}{'':>9s}"
    row += f"  {total_hits}/{total:>3d}"
    print(row)

print()
print("=== Mean z-score on the correct candidate (when scored) ===")
for name, _ in models:
    all_z = []
    for c in cats:
        for _, _, z in results[name][c]:
            if z is not None:
                all_z.append(z)
    if all_z:
        print(f"{name:10s}  mean_z={sum(all_z)/len(all_z):+.3f}  median_z={sorted(all_z)[len(all_z)//2]:+.3f}  n={len(all_z)}")
