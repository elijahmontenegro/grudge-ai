"""Live-service smoke test — TEI on :8080 + vLLM on :8000.

Re-runs run_eval_paths.py's R2 single-config (Qwen3-Embedding-0.6B + zerank-1-small)
against the LIVE Docker services (not isolated-model loaders). Compares against
the prior isolated-model result of 29/31 stage-2 top-1.

If parity holds, the substrate is correctly wired through the live HTTP path
end-to-end (TEI /embed + Qwen3 instruct prefix + vLLM /classify score-model
serving + raw "Yes"-logit head + sigmoid(logit/5) recipe — the same path
core/adapter/zerank/zerank.go scores through in production).
"""
import json
import math
import time
from collections import defaultdict
from pathlib import Path

import numpy as np
import requests

PAIRS_FILE = Path(__file__).parent.parent / "rrc" / "calibrate" / "seed" / "pairs.json"
TEI_URL = "http://localhost:8080"
VLLM_URL = "http://localhost:8000"
RERANK_TOP_K = 64

QWEN3_INSTRUCT = (
    "Instruct: Given a follow-up message in a conversation, "
    "retrieve the prior message that contains the prerequisite information "
    "needed to answer it\nQuery: "
)


def embed(texts, prefix=""):
    """Call TEI /embed; optionally prepend prefix per text."""
    if prefix:
        texts = [prefix + t for t in texts]
    r = requests.post(f"{TEI_URL}/embed",
                      json={"inputs": texts, "truncate": True},
                      timeout=60)
    r.raise_for_status()
    return np.asarray(r.json(), dtype=np.float32)


def rerank_scores(query, candidates):
    """One batched vLLM /classify call mirroring core/adapter/zerank/zerank.go.

    zerank is served as a vLLM SCORE model (re-headed to sequence
    classification whose single classifier weight is the "Yes" lm_head
    row), so the pooled /classify logit IS the raw "Yes" logit.
    use_activation=false returns it raw (vLLM's built-in sigmoid lacks
    the /5 temperature and saturates); the model card's sigmoid(logit/5)
    maps it to [0,1]. Prompts carry the generation suffix, byte-identical
    to apply_chat_template(add_generation_prompt=True) — vLLM's own
    messages mode would pool the wrong position. This replaced the dead
    pre-score-model recipe (/v1/chat/completions + Yes-token logprob),
    which 404s under score-model serving.
    """
    inputs = [
        "<|im_start|>system\n" + query + "<|im_end|>\n"
        "<|im_start|>user\n" + doc + "<|im_end|>\n"
        "<|im_start|>assistant\n"
        for doc in candidates
    ]
    r = requests.post(f"{VLLM_URL}/classify", json={
        "model": "zeroentropy/zerank-1-small",
        "input": inputs,
        "use_activation": False,
    }, timeout=120)
    r.raise_for_status()
    return [1.0 / (1.0 + math.exp(-d["probs"][0] / 5.0)) for d in r.json()["data"]]


def main():
    with PAIRS_FILE.open() as f:
        data = json.load(f)
    all_pairs = []
    for cat, pairs in data["categories"].items():
        for p in pairs:
            all_pairs.append({**p, "cat": cat})
    all_corrects = [p["correct"] for p in all_pairs]
    correct_idx = {p["id"]: i for i, p in enumerate(all_pairs)}

    # Pre-embed all corrects (document side, no prefix).
    print(f"embedding {len(all_corrects)} corrects via TEI ...", flush=True)
    correct_embs = embed(all_corrects)
    print(f"  shape={correct_embs.shape}", flush=True)

    results = {"stage1": defaultdict(list), "stage2": defaultdict(list)}
    for p in all_pairs:
        cat, qid = p["cat"], p["id"]
        ci = correct_idx[qid]

        t0 = time.time()
        q_emb = embed([p["query"]], prefix=QWEN3_INSTRUCT)[0]
        d_embs = embed(p["distractors"])
        c_emb = correct_embs[ci]

        # Stage 1: 5-way, rerank all
        s1_texts = [p["correct"]] + p["distractors"]
        s1_l2 = rerank_scores(p["query"], s1_texts)
        s1_rank = sorted(range(len(s1_texts)), key=lambda i: -s1_l2[i]).index(0) + 1

        # Stage 2: 29-way, dense top-K then rerank
        other_idx = [i for i in range(len(all_corrects)) if i != ci]
        other_embs = correct_embs[other_idx]
        s2_texts = [p["correct"]] + p["distractors"] + [all_corrects[i] for i in other_idx]
        s2_l1_embs = np.vstack([c_emb[None, :], d_embs, other_embs])
        s2_l1 = s2_l1_embs @ q_emb
        topk_idx = np.argsort(-s2_l1)[:RERANK_TOP_K].tolist()
        topk_texts = [s2_texts[i] for i in topk_idx]
        s2_l2 = rerank_scores(p["query"], topk_texts)
        s2_reranked = sorted(zip(topk_idx, s2_l2), key=lambda x: -x[1])
        s2_order = [i for i, _ in s2_reranked]
        s2_rank = s2_order.index(0) + 1 if 0 in s2_order else len(s2_texts)

        ms = (time.time() - t0) * 1000
        results["stage1"][cat].append((qid, s1_rank, ms))
        results["stage2"][cat].append((qid, s2_rank, ms))
        print(f"  {cat[:3]} {qid:18s} S1=#{s1_rank}/5  S2=#{s2_rank}/29  {ms:.0f}ms", flush=True)

    # Tally
    cats = sorted(data["categories"].keys())
    print("\n=== LIVE smoke vs isolated-model parity check ===")
    print(f"{'category':25s}  {'S1 top1':>10s}  {'S2 top1':>10s}  {'S2 r@5':>10s}")
    s1_total = s2_total = s2_r5_total = 0
    n_total = 0
    for c in cats:
        s1_rows = results["stage1"][c]
        s2_rows = results["stage2"][c]
        n = len(s1_rows)
        s1_top = sum(1 for _, r, _ in s1_rows if r == 1)
        s2_top = sum(1 for _, r, _ in s2_rows if r == 1)
        s2_r5 = sum(1 for _, r, _ in s2_rows if r <= 5)
        s1_total += s1_top; s2_total += s2_top; s2_r5_total += s2_r5; n_total += n
        print(f"{c:25s}  {s1_top}/{n:<8d}  {s2_top}/{n:<8d}  {s2_r5}/{n}")
    print(f"{'OVERALL':25s}  {s1_total}/{n_total:<8d}  {s2_total}/{n_total:<8d}  {s2_r5_total}/{n_total}")
    print(f"\nIsolated-model R2 baseline: 30/31 S1 top-1, 29/31 S2 top-1, 31/31 S2 r@5")


if __name__ == "__main__":
    main()
