"""Bi-encoder embedder interrogation: 5-way discrimination + cross-pollinated recall.

Each model embeds query and candidates separately; ranks by cosine similarity.
All embeddings L2-normalized so cosine = dot product.

Stage 1 (within-thread shape):
  For each pair, candidates = [correct] + 4 distractors. Top-1 hit if correct ranks first.

Stage 2 (cross-thread shape):
  For each pair, candidates = [correct] + 4 distractors + 24 other-pair corrects (29 total).
  recall@{1, 5, 10} measured. Tests false-positive resistance against bulk noise from
  topically-distinct prerequisite chunks pulled from other queries — a structural proxy
  for cross-thread retrieval.

Each model uses its documented standard inference path:
  - bge-m3            → SentenceTransformer, symmetric (no instruction)
  - Qwen3-Embedding-* → SentenceTransformer, manual "Instruct: ...\nQuery: ..." prefix on queries
  - NV-Embed-v2       → SentenceTransformer w/ trust_remote_code, manual instruction prefix
  - stella-1.5B-v5    → SentenceTransformer, prompt_name="s2p_query" for queries
  - gte-Qwen2-7B-it   → SentenceTransformer, manual instruction prefix on queries

Models loaded sequentially. 7B+ models may not fit a 12GB GPU at bf16 — log and continue.
"""
import json, time, gc
from collections import defaultdict
from pathlib import Path

import torch
import numpy as np

PAIRS_FILE = Path(__file__).parent / "pairs.json"
with PAIRS_FILE.open() as f:
    data = json.load(f)

all_pairs = []
for cat, pairs in data["categories"].items():
    for p in pairs:
        all_pairs.append({**p, "cat": cat})

ALL_CORRECTS = [p["correct"] for p in all_pairs]
CORRECT_IDX = {p["id"]: i for i, p in enumerate(all_pairs)}

# Task instruction used for asymmetric instruct-tuned embedders.
# Framed as RRC's actual task: prerequisite retrieval in conversational context.
TASK = ("Given a follow-up message in a conversation, retrieve the prior message "
        "that contains the prerequisite information needed to answer it")


# === Loaders ===
# Each returns (model, embed_query_fn, embed_passage_fn).
# Each fn takes list[str] -> np.ndarray (n, D), L2-normalized.

def load_bge_m3():
    from sentence_transformers import SentenceTransformer
    m = SentenceTransformer("BAAI/bge-m3", device="cuda",
                            model_kwargs={"torch_dtype": torch.bfloat16})
    def enc(texts):
        return m.encode(texts, normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    return m, enc, enc  # symmetric


def _qwen_emb_loader(hf_id):
    from sentence_transformers import SentenceTransformer
    m = SentenceTransformer(hf_id, device="cuda",
                            model_kwargs={"torch_dtype": torch.bfloat16},
                            trust_remote_code=True)
    def embed_q(texts):
        formatted = [f"Instruct: {TASK}\nQuery: {t}" for t in texts]
        return m.encode(formatted, normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    def embed_p(texts):
        return m.encode(texts, normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    return m, embed_q, embed_p

load_qwen3_06b = lambda: _qwen_emb_loader("Qwen/Qwen3-Embedding-0.6B")
load_qwen3_4b  = lambda: _qwen_emb_loader("Qwen/Qwen3-Embedding-4B")
load_qwen3_8b  = lambda: _qwen_emb_loader("Qwen/Qwen3-Embedding-8B")


def load_nv_embed_v2():
    from sentence_transformers import SentenceTransformer
    m = SentenceTransformer("nvidia/NV-Embed-v2", trust_remote_code=True, device="cuda",
                            model_kwargs={"torch_dtype": torch.bfloat16})
    qprefix = f"Instruct: {TASK}\nQuery: "
    def embed_q(texts):
        prefixed = [qprefix + t for t in texts]
        return m.encode(prefixed, normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    def embed_p(texts):
        return m.encode(texts, normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    return m, embed_q, embed_p


def load_stella():
    from sentence_transformers import SentenceTransformer
    m = SentenceTransformer("dunzhang/stella_en_1.5B_v5", trust_remote_code=True, device="cuda",
                            model_kwargs={"torch_dtype": torch.bfloat16})
    def embed_q(texts):
        return m.encode(texts, prompt_name="s2p_query", normalize_embeddings=True,
                        convert_to_numpy=True, show_progress_bar=False)
    def embed_p(texts):
        return m.encode(texts, normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    return m, embed_q, embed_p


def load_gte_qwen2_7b():
    from sentence_transformers import SentenceTransformer
    m = SentenceTransformer("Alibaba-NLP/gte-Qwen2-7B-instruct", trust_remote_code=True,
                            device="cuda", model_kwargs={"torch_dtype": torch.bfloat16})
    def embed_q(texts):
        formatted = [f"Instruct: {TASK}\nQuery: {t}" for t in texts]
        return m.encode(formatted, normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    def embed_p(texts):
        return m.encode(texts, normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    return m, embed_q, embed_p


MODELS = [
    ("bge-m3",         "BAAI/bge-m3",                        load_bge_m3),
    ("qwen3-emb-0.6b", "Qwen/Qwen3-Embedding-0.6B",          load_qwen3_06b),
    ("qwen3-emb-4b",   "Qwen/Qwen3-Embedding-4B",            load_qwen3_4b),
    ("qwen3-emb-8b",   "Qwen/Qwen3-Embedding-8B",            load_qwen3_8b),
    ("nv-embed-v2",    "nvidia/NV-Embed-v2",                 load_nv_embed_v2),
    ("stella-1.5b",    "dunzhang/stella_en_1.5B_v5",         load_stella),
    ("gte-qwen2-7b",   "Alibaba-NLP/gte-Qwen2-7B-instruct",  load_gte_qwen2_7b),
]


# === Eval ===
def evaluate_model(short, embed_query, embed_passage):
    print(f"  embedding {len(ALL_CORRECTS)} cross-pool corrects...", flush=True)
    correct_embs = embed_passage(ALL_CORRECTS)  # (25, D)
    D = correct_embs.shape[1]
    print(f"  D={D}", flush=True)

    results = {"stage1": defaultdict(list), "stage2": defaultdict(list)}

    for p in all_pairs:
        cat = p["cat"]
        qid = p["id"]

        t0 = time.time()
        q_emb = embed_query([p["query"]])[0]              # (D,)
        d_embs = embed_passage(p["distractors"])           # (4, D)
        ms = (time.time() - t0) * 1000

        ci = CORRECT_IDX[qid]
        c_emb = correct_embs[ci]

        # Stage 1: 5-way (correct + 4 distractors)
        s1_cands = np.vstack([c_emb[None, :], d_embs])     # (5, D)
        s1_scores = s1_cands @ q_emb                        # (5,)
        s1_rank = int(np.argsort(-s1_scores).tolist().index(0)) + 1

        # Stage 2: cross-pollinated (correct + 4 distractors + 24 other-corrects)
        other_idx = [i for i in range(len(ALL_CORRECTS)) if i != ci]
        other_embs = correct_embs[other_idx]                # (24, D)
        s2_cands = np.vstack([c_emb[None, :], d_embs, other_embs])  # (29, D)
        s2_scores = s2_cands @ q_emb
        s2_rank = int(np.argsort(-s2_scores).tolist().index(0)) + 1

        results["stage1"][cat].append((qid, s1_rank, float(s1_scores[0]), ms))
        results["stage2"][cat].append((qid, s2_rank, float(s2_scores[0]), ms))

        print(f"  {cat[:3]} {qid:18s} S1=#{s1_rank}/5  S2=#{s2_rank}/29  "
              f"s1_score={s1_scores[0]:+.4f}  {ms:5.0f}ms", flush=True)

    return results


all_results = {}
for short, hf_id, loader in MODELS:
    print(f"\n=== {short} ({hf_id}) ===", flush=True)
    print("loading...", flush=True)
    t0 = time.time()
    try:
        model, eq, ep = loader()
    except Exception as e:
        print(f"LOAD FAILED: {type(e).__name__}: {str(e)[:300]}", flush=True)
        all_results[short] = None
        continue
    print(f"loaded in {time.time()-t0:.1f}s, mem={torch.cuda.memory_allocated()/1e9:.2f}GB", flush=True)

    try:
        all_results[short] = evaluate_model(short, eq, ep)
    except Exception as e:
        print(f"EVAL FAILED: {type(e).__name__}: {str(e)[:300]}", flush=True)
        all_results[short] = None

    del model
    try: del eq
    except: pass
    try: del ep
    except: pass
    gc.collect()
    torch.cuda.empty_cache()
    torch.cuda.synchronize()
    print(f"unloaded, mem={torch.cuda.memory_allocated()/1e9:.2f}GB", flush=True)


# === Summary ===
cats = sorted(data["categories"].keys())

print("\n\n=== Stage 1: 5-way top-1 hit (within-thread shape) ===", flush=True)
print(f"{'model':16s}  " + "  ".join(f"{c[:14]:>14s}" for c in cats) + f"  {'OVERALL':>10s}  {'mean_ms':>8s}")
for short, _, _ in MODELS:
    res = all_results.get(short)
    if res is None:
        print(f"{short:16s}  (failed)")
        continue
    row = []
    total_hits = total = 0
    all_ms = []
    for c in cats:
        rows = res["stage1"][c]
        n = len(rows)
        hits = sum(1 for _, r, _, _ in rows if r == 1)
        total_hits += hits
        total += n
        row.append(f"{hits}/{n}")
        for _, _, _, ms in rows:
            all_ms.append(ms)
    mms = sum(all_ms) / len(all_ms) if all_ms else 0
    cell_str = "  ".join(f"{r:>14s}" for r in row)
    print(f"{short:16s}  {cell_str}  {total_hits}/{total:<7d}  {mms:>7.0f}ms")

print("\n\n=== Stage 2: cross-pollinated recall@K (29-candidate pool) ===", flush=True)
for k in [1, 5, 10]:
    print(f"\n--- recall@{k} ---")
    print(f"{'model':16s}  " + "  ".join(f"{c[:14]:>14s}" for c in cats) + f"  {'OVERALL':>10s}")
    for short, _, _ in MODELS:
        res = all_results.get(short)
        if res is None:
            print(f"{short:16s}  (failed)")
            continue
        row = []
        total_hits = total = 0
        for c in cats:
            rows = res["stage2"][c]
            n = len(rows)
            hits = sum(1 for _, r, _, _ in rows if r <= k)
            total_hits += hits
            total += n
            row.append(f"{hits}/{n}")
        cell_str = "  ".join(f"{r:>14s}" for r in row)
        print(f"{short:16s}  {cell_str}  {total_hits}/{total}")


out_path = Path(__file__).parent / "results_embed.json"
serializable = {}
for short, _, _ in MODELS:
    res = all_results.get(short)
    serializable[short] = (None if res is None else
                            {stage: {c: list(rows) for c, rows in r.items()} for stage, r in res.items()})
with out_path.open("w") as f:
    json.dump(serializable, f, indent=2)
print(f"\nraw results -> {out_path}")
