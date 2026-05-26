"""Bi-encoder embedder interrogation v2 — full GPU-fit cross-family lineup.

Each model uses its own documented inference path. Embeddings L2-normalized so
cosine = dot product.

Stage 1 (within-thread shape):
  For each pair, candidates = [correct] + 4 distractors. Top-1 hit if correct ranks first.

Stage 2 (cross-thread shape):
  For each pair, candidates = [correct] + 4 distractors + 24 other-pair corrects (29 total).
  recall@{1, 5, 10}.

Models, all bf16, all fitting 12GB GPU at native precision (no spillover):
  - jinaai/jina-embeddings-v5-text-small  (0.6B) — task+prompt_name asymmetric
  - jinaai/jina-embeddings-v5-text-nano   (0.1B) — same recipe
  - microsoft/harrier-oss-v1-0.6b         (0.6B) — prompt_name="web_search_query" on queries
  - microsoft/harrier-oss-v1-270m         (0.27B) — same recipe
  - Qwen/Qwen3-Embedding-4B               (4B)   — manual "Instruct:\nQuery:" prefix on queries
  - codefuse-ai/F2LLM-v2-4B               (4B)   — encode_query/encode_document
  - codefuse-ai/F2LLM-v2-1.7B             (1.7B) — same recipe
  - zeroentropy/zembed-1                  (4B)   — encode_query/encode_document, distilled from zerank-2
  - google/embeddinggemma-300m            (0.3B) — encode_query/encode_document, MRL dim 768
  - BAAI/bge-m3                           (0.6B) — symmetric baseline
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

QWEN_TASK = ("Given a follow-up message in a conversation, retrieve the prior message "
             "that contains the prerequisite information needed to answer it")


# === Loaders ===
# Each returns (model, embed_query_fn, embed_passage_fn).
# Each fn takes list[str] -> np.ndarray (n, D), L2-normalized.

def _load_st(hf_id, trust=False):
    from sentence_transformers import SentenceTransformer
    return SentenceTransformer(hf_id, device="cuda",
                               model_kwargs={"torch_dtype": torch.bfloat16},
                               trust_remote_code=trust)


def load_bge_m3():
    m = _load_st("BAAI/bge-m3")
    def enc(texts):
        return m.encode(texts, normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    return m, enc, enc


def load_qwen3_4b():
    m = _load_st("Qwen/Qwen3-Embedding-4B", trust=True)
    def embed_q(texts):
        formatted = [f"Instruct: {QWEN_TASK}\nQuery: {t}" for t in texts]
        return m.encode(formatted, normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    def embed_p(texts):
        return m.encode(texts, normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    return m, embed_q, embed_p


def _load_jina_v5(hf_id):
    m = _load_st(hf_id, trust=True)
    def embed_q(texts):
        return m.encode(texts, task="retrieval", prompt_name="query",
                        normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    def embed_p(texts):
        return m.encode(texts, task="retrieval", prompt_name="document",
                        normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    return m, embed_q, embed_p

load_jina_v5_small = lambda: _load_jina_v5("jinaai/jina-embeddings-v5-text-small")
load_jina_v5_nano  = lambda: _load_jina_v5("jinaai/jina-embeddings-v5-text-nano")


def _load_harrier(hf_id):
    m = _load_st(hf_id, trust=True)
    def embed_q(texts):
        return m.encode(texts, prompt_name="web_search_query",
                        normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    def embed_p(texts):
        return m.encode(texts, normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    return m, embed_q, embed_p

load_harrier_06b  = lambda: _load_harrier("microsoft/harrier-oss-v1-0.6b")
load_harrier_270m = lambda: _load_harrier("microsoft/harrier-oss-v1-270m")


def _load_asym_native(hf_id, trust=False):
    """Models with model.encode_query() / model.encode_document() asymmetric methods.
    Used by F2LLM-v2, zembed-1, embeddinggemma."""
    m = _load_st(hf_id, trust=trust)
    def embed_q(texts):
        out = m.encode_query(texts, normalize_embeddings=True, convert_to_numpy=True,
                             show_progress_bar=False)
        # encode_query may return 1D for single input; normalize shape
        if out.ndim == 1:
            out = out[None, :]
        return out
    def embed_p(texts):
        out = m.encode_document(texts, normalize_embeddings=True, convert_to_numpy=True,
                                show_progress_bar=False)
        if out.ndim == 1:
            out = out[None, :]
        return out
    return m, embed_q, embed_p

load_f2llm_4b   = lambda: _load_asym_native("codefuse-ai/F2LLM-v2-4B")
load_f2llm_17b  = lambda: _load_asym_native("codefuse-ai/F2LLM-v2-1.7B")
load_zembed_1   = lambda: _load_asym_native("zeroentropy/zembed-1", trust=True)


def load_embeddinggemma_300m():
    """EmbeddingGemma activations do not support float16 — use bf16 (we already use bf16)."""
    m = _load_st("google/embeddinggemma-300m")
    def embed_q(texts):
        out = m.encode_query(texts, normalize_embeddings=True, convert_to_numpy=True,
                             show_progress_bar=False)
        if out.ndim == 1:
            out = out[None, :]
        return out
    def embed_p(texts):
        out = m.encode_document(texts, normalize_embeddings=True, convert_to_numpy=True,
                                show_progress_bar=False)
        if out.ndim == 1:
            out = out[None, :]
        return out
    return m, embed_q, embed_p


MODELS = [
    ("bge-m3",          "BAAI/bge-m3",                          load_bge_m3),
    ("emb-gemma-300m",  "google/embeddinggemma-300m",           load_embeddinggemma_300m),
    ("jina-v5-nano",    "jinaai/jina-embeddings-v5-text-nano",  load_jina_v5_nano),
    ("jina-v5-small",   "jinaai/jina-embeddings-v5-text-small", load_jina_v5_small),
    ("harrier-270m",    "microsoft/harrier-oss-v1-270m",        load_harrier_270m),
    ("harrier-0.6b",    "microsoft/harrier-oss-v1-0.6b",        load_harrier_06b),
    ("f2llm-1.7b",      "codefuse-ai/F2LLM-v2-1.7B",            load_f2llm_17b),
    ("f2llm-4b",        "codefuse-ai/F2LLM-v2-4B",              load_f2llm_4b),
    ("qwen3-emb-4b",    "Qwen/Qwen3-Embedding-4B",              load_qwen3_4b),
    ("zembed-1",        "zeroentropy/zembed-1",                 load_zembed_1),
]


# === Eval ===
def evaluate_model(short, embed_query, embed_passage):
    print(f"  embedding {len(ALL_CORRECTS)} cross-pool corrects...", flush=True)
    correct_embs = embed_passage(ALL_CORRECTS)
    D = correct_embs.shape[1]
    print(f"  D={D}", flush=True)

    results = {"stage1": defaultdict(list), "stage2": defaultdict(list)}

    for p in all_pairs:
        cat = p["cat"]
        qid = p["id"]

        t0 = time.time()
        q_emb = embed_query([p["query"]])[0]
        d_embs = embed_passage(p["distractors"])
        ms = (time.time() - t0) * 1000

        ci = CORRECT_IDX[qid]
        c_emb = correct_embs[ci]

        s1_cands = np.vstack([c_emb[None, :], d_embs])
        s1_scores = s1_cands @ q_emb
        s1_rank = int(np.argsort(-s1_scores).tolist().index(0)) + 1

        other_idx = [i for i in range(len(ALL_CORRECTS)) if i != ci]
        other_embs = correct_embs[other_idx]
        s2_cands = np.vstack([c_emb[None, :], d_embs, other_embs])
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

print("\n\n=== Stage 1: 5-way top-1 hit ===", flush=True)
print(f"{'model':18s}  " + "  ".join(f"{c[:14]:>14s}" for c in cats) + f"  {'OVERALL':>10s}  {'mean_ms':>8s}")
for short, _, _ in MODELS:
    res = all_results.get(short)
    if res is None:
        print(f"{short:18s}  (failed)")
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
    print(f"{short:18s}  {cell_str}  {total_hits}/{total:<7d}  {mms:>7.0f}ms")

print("\n\n=== Stage 2: cross-pollinated recall@K (29-candidate pool) ===", flush=True)
for k in [1, 5, 10]:
    print(f"\n--- recall@{k} ---")
    print(f"{'model':18s}  " + "  ".join(f"{c[:14]:>14s}" for c in cats) + f"  {'OVERALL':>10s}")
    for short, _, _ in MODELS:
        res = all_results.get(short)
        if res is None:
            print(f"{short:18s}  (failed)")
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
        print(f"{short:18s}  {cell_str}  {total_hits}/{total}")


out_path = Path(__file__).parent / "results_embed_v2.json"
serializable = {}
for short, _, _ in MODELS:
    res = all_results.get(short)
    serializable[short] = (None if res is None else
                            {stage: {c: list(rows) for c, rows in r.items()} for stage, r in res.items()})
with out_path.open("w") as f:
    json.dump(serializable, f, indent=2)
print(f"\nraw results -> {out_path}")
