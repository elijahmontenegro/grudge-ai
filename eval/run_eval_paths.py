"""Multi-path eval: P1 (dense + dedicated reranker), P2 (multi-vector single-stage),
P3 (dense retrieve + multi-vector rerank live-encoded).

Tests against pairs.json (now extended with F_long_document for the information-density
failure mode that distinguishes single-vector from multi-vector). Measures top-1, recall@K,
peak VRAM under co-resident loading, and per-turn latency.

12GB GPU target. All candidate model pairings are sized to fit ≤10GB combined under bf16
inference, leaving headroom for KV cache + CUDA graphs. Pairings that didn't fit last
session (zembed-1 4B + zerank-2 4B = 8B) are intentionally not in the candidate set.

All listed candidates are Apache 2.0 licensed.

Run:
    cd rerank_eval/ && python run_eval_paths.py

Dependencies (one-time):
    pip install torch sentence-transformers transformers numpy
    pip install pylate                  # for multi-vector / ColBERT paths
    pip install accelerate bitsandbytes  # transformers utilities

Output: results_paths.json + console summary.
"""

from __future__ import annotations

import gc
import json
import math
import time
from collections import defaultdict
from pathlib import Path

import numpy as np
import torch


PAIRS_FILE = Path(__file__).parent / "pairs.json"
OUT_FILE = Path(__file__).parent / "results_paths.json"
RERANK_TOP_K = 64  # RRC's RerankTopK default; rerank everything ≤ this many candidates

with PAIRS_FILE.open() as f:
    data = json.load(f)

all_pairs = []
for cat, pairs in data["categories"].items():
    for p in pairs:
        all_pairs.append({**p, "cat": cat})

ALL_CORRECTS = [p["correct"] for p in all_pairs]
CORRECT_IDX = {p["id"]: i for i, p in enumerate(all_pairs)}

QWEN_INSTRUCT = ("Given a follow-up message in a conversation, retrieve the prior message "
                 "that contains the prerequisite information needed to answer it")


# ============================================================================
# Layer 1 — dense bi-encoder loaders (single-vector retrieval)
# ============================================================================

def load_dense_qwen3_emb_06b():
    """Qwen3-Embedding-0.6B — Apache 2.0, ~1.2GB bf16."""
    from sentence_transformers import SentenceTransformer
    m = SentenceTransformer(
        "Qwen/Qwen3-Embedding-0.6B",
        device="cuda",
        model_kwargs={"torch_dtype": torch.bfloat16},
        trust_remote_code=True,
    )
    def embed_q(texts):
        formatted = [f"Instruct: {QWEN_INSTRUCT}\nQuery: {t}" for t in texts]
        return m.encode(formatted, normalize_embeddings=True,
                        convert_to_numpy=True, show_progress_bar=False)
    def embed_p(texts):
        return m.encode(texts, normalize_embeddings=True,
                        convert_to_numpy=True, show_progress_bar=False)
    return m, embed_q, embed_p


def load_dense_jina_v5_small():
    """jina-embeddings-v5-text-small — Apache 2.0, ~1.2GB bf16, matryoshka."""
    from sentence_transformers import SentenceTransformer
    m = SentenceTransformer(
        "jinaai/jina-embeddings-v5-text-small",
        device="cuda",
        model_kwargs={"torch_dtype": torch.bfloat16},
        trust_remote_code=True,
    )
    def embed_q(texts):
        return m.encode(texts, task="retrieval", prompt_name="query",
                        normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    def embed_p(texts):
        return m.encode(texts, task="retrieval", prompt_name="document",
                        normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    return m, embed_q, embed_p


def load_dense_harrier_06b():
    """harrier-oss-v1-0.6b — open license."""
    from sentence_transformers import SentenceTransformer
    m = SentenceTransformer(
        "microsoft/harrier-oss-v1-0.6b",
        device="cuda",
        model_kwargs={"torch_dtype": torch.bfloat16},
        trust_remote_code=True,
    )
    def embed_q(texts):
        return m.encode(texts, prompt_name="web_search_query",
                        normalize_embeddings=True, convert_to_numpy=True,
                        show_progress_bar=False)
    def embed_p(texts):
        return m.encode(texts, normalize_embeddings=True,
                        convert_to_numpy=True, show_progress_bar=False)
    return m, embed_q, embed_p


# ============================================================================
# Layer 2 — dedicated reranker loaders (LLM-judge style)
# ============================================================================

def load_rerank_qwen3_06b():
    """Qwen3-Reranker-0.6B — Apache 2.0, ~1.2GB bf16, LLM-judge with yes/no logits."""
    from transformers import AutoModelForCausalLM, AutoTokenizer
    hf_id = "Qwen/Qwen3-Reranker-0.6B"
    tok = AutoTokenizer.from_pretrained(hf_id, padding_side="left")
    if tok.pad_token is None:
        tok.pad_token = tok.eos_token
    model = AutoModelForCausalLM.from_pretrained(
        hf_id, torch_dtype=torch.bfloat16, device_map="cuda")
    model.eval()
    yes_id = tok("yes", add_special_tokens=False).input_ids[-1]
    no_id = tok("no", add_special_tokens=False).input_ids[-1]

    def score(query, candidates):
        # Per Qwen3-Reranker recipe: yes/no token softmax at the answer position.
        out = []
        for cand in candidates:
            instr = QWEN_INSTRUCT
            prompt = (f"<Instruct>: {instr}\n<Query>: {query}\n<Document>: {cand}")
            messages = [
                {"role": "system",
                 "content": "Judge whether the Document meets the requirements based on the Query "
                            "and the Instruct provided. Note that the answer can only be 'yes' or 'no'."},
                {"role": "user", "content": prompt},
            ]
            text = tok.apply_chat_template(messages, tokenize=False, add_generation_prompt=True)
            text = text + "<think>\n\n</think>\n\n"
            inputs = tok(text, return_tensors="pt", truncation=True, max_length=8192).to("cuda")
            with torch.no_grad():
                logits = model(**inputs, use_cache=False).logits
            last_logits = logits[0, -1].float()
            yes_l = float(last_logits[yes_id])
            no_l = float(last_logits[no_id])
            score_val = math.exp(yes_l) / (math.exp(yes_l) + math.exp(no_l))
            out.append(score_val)
        return out
    return model, score


def load_rerank_zerank_1_small():
    """zerank-1-small — Apache 2.0, 1.7B params, ~3.4GB bf16, LLM-judge yes-token."""
    from transformers import AutoModelForCausalLM, AutoTokenizer
    hf_id = "zeroentropy/zerank-1-small"
    tok = AutoTokenizer.from_pretrained(hf_id, padding_side="right")
    if tok.pad_token is None:
        tok.pad_token = tok.eos_token
    model = AutoModelForCausalLM.from_pretrained(
        hf_id, torch_dtype=torch.bfloat16, device_map="cuda")
    model.eval()
    yes_id = tok.encode("Yes", add_special_tokens=False)[0]

    def score(query, candidates):
        out = []
        for cand in candidates:
            messages = [
                {"role": "system", "content": query.strip()},
                {"role": "user", "content": cand.strip()},
            ]
            text = tok.apply_chat_template(messages, tokenize=False, add_generation_prompt=True)
            inputs = tok(text, return_tensors="pt", truncation=True, max_length=8192).to("cuda")
            with torch.no_grad():
                logits = model(**inputs, use_cache=False).logits
            attention_mask = inputs.attention_mask
            last_pos = int(attention_mask.sum(dim=1) - 1)
            last_logits = logits[0, last_pos].float()
            yes_logit = float(last_logits[yes_id]) / 5.0
            score_val = 1.0 / (1.0 + math.exp(-yes_logit))
            out.append(score_val)
        return out
    return model, score


def load_rerank_zerank_2():
    """zerank-2 — CC-BY-NC-4.0, 4B params, ~8GB bf16. Same yes-token recipe as
    zerank-1-small. NC license: research only at this stage; license decision
    deferred per substrate plan."""
    from transformers import AutoModelForCausalLM, AutoTokenizer
    hf_id = "zeroentropy/zerank-2"
    tok = AutoTokenizer.from_pretrained(hf_id, padding_side="right")
    if tok.pad_token is None:
        tok.pad_token = tok.eos_token
    model = AutoModelForCausalLM.from_pretrained(
        hf_id, torch_dtype=torch.bfloat16, device_map="cuda")
    model.eval()
    yes_id = tok.encode("Yes", add_special_tokens=False)[0]

    def score(query, candidates):
        out = []
        for cand in candidates:
            messages = [
                {"role": "system", "content": query.strip()},
                {"role": "user", "content": cand.strip()},
            ]
            text = tok.apply_chat_template(messages, tokenize=False, add_generation_prompt=True)
            inputs = tok(text, return_tensors="pt", truncation=True, max_length=8192).to("cuda")
            with torch.no_grad():
                logits = model(**inputs, use_cache=False).logits
            attention_mask = inputs.attention_mask
            last_pos = int(attention_mask.sum(dim=1) - 1)
            last_logits = logits[0, last_pos].float()
            yes_logit = float(last_logits[yes_id]) / 5.0
            score_val = 1.0 / (1.0 + math.exp(-yes_logit))
            out.append(score_val)
        return out
    return model, score


def load_dense_zembed_1():
    """zembed-1 — CC-BY-NC-4.0, 4B params, ~8GB bf16. Asymmetric encoder, dim 2560."""
    from sentence_transformers import SentenceTransformer
    m = SentenceTransformer(
        "zeroentropy/zembed-1",
        device="cuda",
        model_kwargs={"torch_dtype": torch.bfloat16},
        trust_remote_code=True,
    )
    def embed_q(texts):
        out = m.encode_query(texts, normalize_embeddings=True,
                             convert_to_numpy=True, show_progress_bar=False)
        if out.ndim == 1:
            out = out[None, :]
        return out
    def embed_p(texts):
        out = m.encode_document(texts, normalize_embeddings=True,
                                convert_to_numpy=True, show_progress_bar=False)
        if out.ndim == 1:
            out = out[None, :]
        return out
    return m, embed_q, embed_p


# ============================================================================
# Multi-vector ColBERT loaders (P2/P3)
# ============================================================================

def load_colbert_answerai_small():
    """answerai-colbert-small-v1 — Apache 2.0, 33M params, ~70MB bf16. English."""
    from pylate import models, scores
    m = models.ColBERT(model_name_or_path="answerdotai/answerai-colbert-small-v1",
                       device="cuda")
    def encode_q(texts):
        return m.encode(texts, is_query=True, show_progress_bar=False, convert_to_tensor=True)
    def encode_p(texts):
        return m.encode(texts, is_query=False, show_progress_bar=False, convert_to_tensor=True)
    def maxsim(q_vecs, d_vecs_list):
        # q_vecs: (n_q_tokens, dim). d_vecs_list: list of (n_d_tokens, dim).
        out = []
        for d in d_vecs_list:
            sim = q_vecs @ d.T  # (n_q, n_d)
            score = sim.max(dim=1).values.sum().item()
            out.append(score)
        return out
    return m, encode_q, encode_p, maxsim


def load_colbert_sauerkraut():
    """SauerkrautLM-Multi-ModernColBERT — Apache 2.0, multilingual variant."""
    from pylate import models
    m = models.ColBERT(model_name_or_path="VAGOsolutions/SauerkrautLM-Multi-ModernColBERT",
                       device="cuda")
    def encode_q(texts):
        return m.encode(texts, is_query=True, show_progress_bar=False, convert_to_tensor=True)
    def encode_p(texts):
        return m.encode(texts, is_query=False, show_progress_bar=False, convert_to_tensor=True)
    def maxsim(q_vecs, d_vecs_list):
        out = []
        for d in d_vecs_list:
            sim = q_vecs @ d.T
            score = sim.max(dim=1).values.sum().item()
            out.append(score)
        return out
    return m, encode_q, encode_p, maxsim


def load_colbert_jina_v2():
    """jina-colbert-v2 — 560M params, CC-BY-NC-4.0 (research only, not shippable
    as-is). Larger ColBERT to break the capacity confound: the prior P2 tests
    used 33M and 150M models against a 2.3B P1 stack, which isn't a fair
    architecture comparison."""
    from pylate import models
    m = models.ColBERT(model_name_or_path="jinaai/jina-colbert-v2",
                       device="cuda", trust_remote_code=True)
    def encode_q(texts):
        return m.encode(texts, is_query=True, show_progress_bar=False, convert_to_tensor=True)
    def encode_p(texts):
        return m.encode(texts, is_query=False, show_progress_bar=False, convert_to_tensor=True)
    def maxsim(q_vecs, d_vecs_list):
        out = []
        for d in d_vecs_list:
            sim = q_vecs @ d.T
            score = sim.max(dim=1).values.sum().item()
            out.append(score)
        return out
    return m, encode_q, encode_p, maxsim


def load_colbert_gte_modernbert():
    """GTE-ModernColBERT-v1 — LightOn/Alibaba, ~150M, ModernBERT-based, claims
    SOTA on BEIR for late-interaction. License unverified at code time —
    research run only until verified."""
    from pylate import models
    m = models.ColBERT(model_name_or_path="lightonai/GTE-ModernColBERT-v1",
                       device="cuda")
    def encode_q(texts):
        return m.encode(texts, is_query=True, show_progress_bar=False, convert_to_tensor=True)
    def encode_p(texts):
        return m.encode(texts, is_query=False, show_progress_bar=False, convert_to_tensor=True)
    def maxsim(q_vecs, d_vecs_list):
        out = []
        for d in d_vecs_list:
            sim = q_vecs @ d.T
            score = sim.max(dim=1).values.sum().item()
            out.append(score)
        return out
    return m, encode_q, encode_p, maxsim


# ============================================================================
# Eval pipelines
# ============================================================================

def gpu_mem_gb():
    """Current allocated VRAM in GB."""
    return torch.cuda.memory_allocated() / 1e9 if torch.cuda.is_available() else 0.0


def gpu_peak_gb():
    """Peak VRAM in GB since last reset."""
    return torch.cuda.max_memory_allocated() / 1e9 if torch.cuda.is_available() else 0.0


def reset_gpu_stats():
    if torch.cuda.is_available():
        torch.cuda.reset_peak_memory_stats()


def cleanup():
    gc.collect()
    if torch.cuda.is_available():
        torch.cuda.empty_cache()
        torch.cuda.synchronize()
    reset_gpu_stats()


def eval_path_p1(layer1_loader, layer2_loader, l1_name, l2_name):
    """P1: dense bi-encoder retrieval + dedicated reranker."""
    print(f"\n=== P1: {l1_name} + {l2_name} ===", flush=True)
    cleanup()
    reset_gpu_stats()

    print(f"  loading L1={l1_name} ...", flush=True)
    t0 = time.time()
    l1_model, embed_q, embed_p = layer1_loader()
    l1_load_s = time.time() - t0
    l1_mem = gpu_mem_gb()
    print(f"    L1 loaded in {l1_load_s:.1f}s, VRAM={l1_mem:.2f}GB", flush=True)

    print(f"  encoding {len(ALL_CORRECTS)} corrects ...", flush=True)
    correct_embs = embed_p(ALL_CORRECTS)

    print(f"  loading L2={l2_name} (co-resident) ...", flush=True)
    t0 = time.time()
    l2_model, score_fn = layer2_loader()
    l2_load_s = time.time() - t0
    co_resident_mem = gpu_mem_gb()
    co_resident_peak = gpu_peak_gb()
    print(f"    L2 loaded in {l2_load_s:.1f}s. Co-resident VRAM={co_resident_mem:.2f}GB peak={co_resident_peak:.2f}GB", flush=True)

    results = {
        "stage1": defaultdict(list),
        "stage2": defaultdict(list),
        "l1_mem_gb": l1_mem,
        "co_resident_peak_gb": co_resident_peak,
        "l1_load_s": l1_load_s,
        "l2_load_s": l2_load_s,
    }

    for p in all_pairs:
        cat = p["cat"]; qid = p["id"]; ci = CORRECT_IDX[qid]

        # Stage 1: 5-way (correct + 4 distractors)
        t0 = time.time()
        q_emb = embed_q([p["query"]])[0]
        d_embs = embed_p(p["distractors"])
        c_emb = correct_embs[ci]
        s1_texts = [p["correct"]] + p["distractors"]
        s1_l1_embs = np.vstack([c_emb[None, :], d_embs])
        s1_l1_scores = s1_l1_embs @ q_emb
        # All 5 candidates fit under K=64, so rerank everything
        s1_l2_scores = score_fn(p["query"], s1_texts)
        order = sorted(range(len(s1_texts)), key=lambda i: -s1_l2_scores[i])
        s1_rank = order.index(0) + 1  # correct is index 0
        s1_ms = (time.time() - t0) * 1000

        # Stage 2: 29-way (correct + 4 distractors + 24 other corrects)
        t0 = time.time()
        other_idx = [i for i in range(len(ALL_CORRECTS)) if i != ci]
        other_embs = correct_embs[other_idx]
        s2_texts = [p["correct"]] + p["distractors"] + [ALL_CORRECTS[i] for i in other_idx]
        s2_l1_embs = np.vstack([c_emb[None, :], d_embs, other_embs])
        s2_l1_scores = s2_l1_embs @ q_emb
        # Layer 1 picks top-K candidates for rerank
        topk_idx = np.argsort(-s2_l1_scores)[:RERANK_TOP_K].tolist()
        topk_texts = [s2_texts[i] for i in topk_idx]
        s2_l2_scores = score_fn(p["query"], topk_texts)
        s2_reranked = sorted(zip(topk_idx, s2_l2_scores), key=lambda x: -x[1])
        s2_final_order = [i for i, _ in s2_reranked]
        s2_rank = s2_final_order.index(0) + 1 if 0 in s2_final_order else len(s2_texts)
        s2_ms = (time.time() - t0) * 1000

        results["stage1"][cat].append((qid, s1_rank, s1_ms))
        results["stage2"][cat].append((qid, s2_rank, s2_ms))
        print(f"    {cat[:3]} {qid:18s} S1=#{s1_rank}/5  S2=#{s2_rank}/29  s1={s1_ms:.0f}ms s2={s2_ms:.0f}ms",
              flush=True)

    del l1_model, l2_model, embed_q, embed_p, score_fn
    cleanup()
    return results


def eval_path_p1_sequential(layer1_loader, layer2_loader, l1_name, l2_name):
    """P1 with sequential model loading: L1 → encode/score → unload → L2 → rerank → unload.

    Used for pairings where co-resident loading exceeds 12GB (e.g. zembed-1 + zerank-2,
    each ~8GB at bf16). Measures quality independently of GPU-fit. Reports L1-only and
    L2-only peak VRAM separately rather than a co-resident peak (which never materializes)."""
    print(f"\n=== P1 (sequential): {l1_name} + {l2_name} ===", flush=True)
    cleanup()
    reset_gpu_stats()

    # ---- Pass 1: Layer 1 ----
    print(f"  loading L1={l1_name} ...", flush=True)
    t0 = time.time()
    l1_model, embed_q, embed_p = layer1_loader()
    l1_load_s = time.time() - t0
    print(f"    L1 loaded in {l1_load_s:.1f}s, VRAM={gpu_mem_gb():.2f}GB", flush=True)

    print(f"  encoding {len(ALL_CORRECTS)} corrects ...", flush=True)
    correct_embs = embed_p(ALL_CORRECTS)

    # Per-pair: encode query + distractors, store dense scores for stage 1 and stage 2.
    per_pair_l1 = {}
    for p in all_pairs:
        qid = p["id"]; ci = CORRECT_IDX[qid]
        q_emb = embed_q([p["query"]])[0]
        d_embs = embed_p(p["distractors"])
        c_emb = correct_embs[ci]

        s1_texts = [p["correct"]] + p["distractors"]
        s1_l1_embs = np.vstack([c_emb[None, :], d_embs])
        s1_l1_scores = (s1_l1_embs @ q_emb).tolist()

        other_idx = [i for i in range(len(ALL_CORRECTS)) if i != ci]
        other_embs = correct_embs[other_idx]
        s2_texts = [p["correct"]] + p["distractors"] + [ALL_CORRECTS[i] for i in other_idx]
        s2_l1_embs = np.vstack([c_emb[None, :], d_embs, other_embs])
        s2_l1_scores = (s2_l1_embs @ q_emb).tolist()

        per_pair_l1[qid] = {
            "s1_texts": s1_texts, "s1_l1_scores": s1_l1_scores,
            "s2_texts": s2_texts, "s2_l1_scores": s2_l1_scores,
        }

    l1_peak = gpu_peak_gb()
    print(f"    L1 peak VRAM={l1_peak:.2f}GB", flush=True)

    # Unload L1
    del l1_model, embed_q, embed_p
    cleanup()
    reset_gpu_stats()

    # ---- Pass 2: Layer 2 ----
    print(f"  loading L2={l2_name} ...", flush=True)
    t0 = time.time()
    l2_model, score_fn = layer2_loader()
    l2_load_s = time.time() - t0
    l2_peak_load = gpu_peak_gb()
    print(f"    L2 loaded in {l2_load_s:.1f}s, peak={l2_peak_load:.2f}GB", flush=True)

    results = {
        "stage1": defaultdict(list),
        "stage2": defaultdict(list),
        "l1_peak_gb": l1_peak,
        "l2_peak_gb": l2_peak_load,
        "co_resident_peak_gb": max(l1_peak, l2_peak_load),  # sequential, not actually co-resident
        "sequential": True,
        "l1_load_s": l1_load_s,
        "l2_load_s": l2_load_s,
    }

    for p in all_pairs:
        cat = p["cat"]; qid = p["id"]
        cached = per_pair_l1[qid]

        # Stage 1: 5-way — rerank everything
        t0 = time.time()
        s1_l2_scores = score_fn(p["query"], cached["s1_texts"])
        order = sorted(range(len(cached["s1_texts"])), key=lambda i: -s1_l2_scores[i])
        s1_rank = order.index(0) + 1
        s1_ms = (time.time() - t0) * 1000

        # Stage 2: 29-way — L1 picks top-K, L2 reranks
        t0 = time.time()
        topk_idx = sorted(range(len(cached["s2_texts"])),
                          key=lambda i: -cached["s2_l1_scores"][i])[:RERANK_TOP_K]
        topk_texts = [cached["s2_texts"][i] for i in topk_idx]
        s2_l2_scores = score_fn(p["query"], topk_texts)
        s2_reranked = sorted(zip(topk_idx, s2_l2_scores), key=lambda x: -x[1])
        s2_final_order = [i for i, _ in s2_reranked]
        s2_rank = s2_final_order.index(0) + 1 if 0 in s2_final_order else len(cached["s2_texts"])
        s2_ms = (time.time() - t0) * 1000

        results["stage1"][cat].append((qid, s1_rank, s1_ms))
        results["stage2"][cat].append((qid, s2_rank, s2_ms))
        print(f"    {cat[:3]} {qid:18s} S1=#{s1_rank}/5  S2=#{s2_rank}/29  s1={s1_ms:.0f}ms s2={s2_ms:.0f}ms",
              flush=True)

    del l2_model, score_fn
    cleanup()
    return results


def eval_path_p2(loader, name):
    """P2: multi-vector ColBERT alone, MaxSim retrieval+rank in one stage."""
    print(f"\n=== P2: {name} (single-stage) ===", flush=True)
    cleanup()
    reset_gpu_stats()

    print(f"  loading {name} ...", flush=True)
    t0 = time.time()
    model, encode_q, encode_p, maxsim = loader()
    load_s = time.time() - t0
    peak_mem = gpu_peak_gb()
    print(f"    loaded in {load_s:.1f}s, peak VRAM={peak_mem:.2f}GB", flush=True)

    print(f"  encoding {len(ALL_CORRECTS)} corrects (multi-vector) ...", flush=True)
    correct_mvs = encode_p(ALL_CORRECTS)
    # correct_mvs is a list of tensors (n_d_tokens, dim) per candidate.

    results = {
        "stage1": defaultdict(list),
        "stage2": defaultdict(list),
        "co_resident_peak_gb": peak_mem,  # single model so this IS the peak
        "load_s": load_s,
    }

    for p in all_pairs:
        cat = p["cat"]; qid = p["id"]; ci = CORRECT_IDX[qid]

        # Stage 1
        t0 = time.time()
        q_mv = encode_q([p["query"]])[0]
        d_mvs = encode_p(p["distractors"])
        c_mv = correct_mvs[ci]
        s1_cands_mvs = [c_mv] + list(d_mvs)
        s1_scores = maxsim(q_mv, s1_cands_mvs)
        s1_rank = sorted(range(len(s1_scores)), key=lambda i: -s1_scores[i]).index(0) + 1
        s1_ms = (time.time() - t0) * 1000

        # Stage 2
        t0 = time.time()
        other_idx = [i for i in range(len(ALL_CORRECTS)) if i != ci]
        s2_cands_mvs = [c_mv] + list(d_mvs) + [correct_mvs[i] for i in other_idx]
        s2_scores = maxsim(q_mv, s2_cands_mvs)
        s2_rank = sorted(range(len(s2_scores)), key=lambda i: -s2_scores[i]).index(0) + 1
        s2_ms = (time.time() - t0) * 1000

        results["stage1"][cat].append((qid, s1_rank, s1_ms))
        results["stage2"][cat].append((qid, s2_rank, s2_ms))
        print(f"    {cat[:3]} {qid:18s} S1=#{s1_rank}/5  S2=#{s2_rank}/29  s1={s1_ms:.0f}ms s2={s2_ms:.0f}ms",
              flush=True)

    del model, encode_q, encode_p, maxsim
    cleanup()
    return results


def eval_path_p3(layer1_loader, colbert_loader, l1_name, mv_name):
    """P3: dense retrieval + multi-vector MaxSim rerank (live encoded). Both co-resident."""
    print(f"\n=== P3: {l1_name} + {mv_name} (cascaded) ===", flush=True)
    cleanup()
    reset_gpu_stats()

    print(f"  loading L1={l1_name} ...", flush=True)
    t0 = time.time()
    l1_model, embed_q, embed_p = layer1_loader()
    l1_load_s = time.time() - t0
    l1_mem = gpu_mem_gb()
    print(f"    L1 loaded in {l1_load_s:.1f}s, VRAM={l1_mem:.2f}GB", flush=True)

    print(f"  encoding {len(ALL_CORRECTS)} corrects (dense) ...", flush=True)
    correct_embs = embed_p(ALL_CORRECTS)

    print(f"  loading L2={mv_name} (co-resident) ...", flush=True)
    t0 = time.time()
    mv_model, mv_encode_q, mv_encode_p, mv_maxsim = colbert_loader()
    l2_load_s = time.time() - t0
    co_resident_mem = gpu_mem_gb()
    co_resident_peak = gpu_peak_gb()
    print(f"    L2 loaded in {l2_load_s:.1f}s. Co-resident VRAM={co_resident_mem:.2f}GB peak={co_resident_peak:.2f}GB",
          flush=True)

    results = {
        "stage1": defaultdict(list),
        "stage2": defaultdict(list),
        "l1_mem_gb": l1_mem,
        "co_resident_peak_gb": co_resident_peak,
        "l1_load_s": l1_load_s,
        "l2_load_s": l2_load_s,
    }

    for p in all_pairs:
        cat = p["cat"]; qid = p["id"]; ci = CORRECT_IDX[qid]

        # Stage 1
        t0 = time.time()
        q_emb = embed_q([p["query"]])[0]
        d_embs = embed_p(p["distractors"])
        c_emb = correct_embs[ci]
        s1_texts = [p["correct"]] + p["distractors"]
        # All 5 fit under K=64, rerank all with multi-vector MaxSim live
        q_mv = mv_encode_q([p["query"]])[0]
        s1_d_mvs = mv_encode_p(s1_texts)
        s1_scores = mv_maxsim(q_mv, list(s1_d_mvs))
        s1_rank = sorted(range(len(s1_texts)), key=lambda i: -s1_scores[i]).index(0) + 1
        s1_ms = (time.time() - t0) * 1000

        # Stage 2
        t0 = time.time()
        other_idx = [i for i in range(len(ALL_CORRECTS)) if i != ci]
        other_embs = correct_embs[other_idx]
        s2_texts = [p["correct"]] + p["distractors"] + [ALL_CORRECTS[i] for i in other_idx]
        s2_l1_embs = np.vstack([c_emb[None, :], d_embs, other_embs])
        s2_l1_scores = s2_l1_embs @ q_emb
        # L1 picks top-K, L2 reranks via MaxSim
        topk_idx = np.argsort(-s2_l1_scores)[:RERANK_TOP_K].tolist()
        topk_texts = [s2_texts[i] for i in topk_idx]
        topk_d_mvs = mv_encode_p(topk_texts)
        topk_scores = mv_maxsim(q_mv, list(topk_d_mvs))
        s2_reranked = sorted(zip(topk_idx, topk_scores), key=lambda x: -x[1])
        s2_final_order = [i for i, _ in s2_reranked]
        s2_rank = s2_final_order.index(0) + 1 if 0 in s2_final_order else len(s2_texts)
        s2_ms = (time.time() - t0) * 1000

        results["stage1"][cat].append((qid, s1_rank, s1_ms))
        results["stage2"][cat].append((qid, s2_rank, s2_ms))
        print(f"    {cat[:3]} {qid:18s} S1=#{s1_rank}/5  S2=#{s2_rank}/29  s1={s1_ms:.0f}ms s2={s2_ms:.0f}ms",
              flush=True)

    del l1_model, mv_model, embed_q, embed_p, mv_encode_q, mv_encode_p, mv_maxsim
    cleanup()
    return results


# ============================================================================
# Driver
# ============================================================================

CONFIGS = [
    # (label, path, runner)
    # P1: dense + dedicated reranker
    ("p1_qwen3-emb-0.6b+qwen3-rerank-0.6b", "P1",
     lambda: eval_path_p1(load_dense_qwen3_emb_06b, load_rerank_qwen3_06b,
                          "Qwen3-Embedding-0.6B", "Qwen3-Reranker-0.6B")),
    ("p1_qwen3-emb-0.6b+zerank-1-small", "P1",
     lambda: eval_path_p1(load_dense_qwen3_emb_06b, load_rerank_zerank_1_small,
                          "Qwen3-Embedding-0.6B", "zerank-1-small")),
    ("p1_jina-v5-small+zerank-1-small", "P1",
     lambda: eval_path_p1(load_dense_jina_v5_small, load_rerank_zerank_1_small,
                          "jina-v5-small", "zerank-1-small")),
    # P2: multi-vector single stage
    ("p2_answerai-colbert-small", "P2",
     lambda: eval_path_p2(load_colbert_answerai_small, "answerai-colbert-small-v1")),
    ("p2_sauerkraut-modern-colbert", "P2",
     lambda: eval_path_p2(load_colbert_sauerkraut, "SauerkrautLM-Multi-ModernColBERT")),
    ("p2_gte-moderncolbert-v1", "P2",
     lambda: eval_path_p2(load_colbert_gte_modernbert, "GTE-ModernColBERT-v1")),
    ("p2_jina-colbert-v2", "P2",
     lambda: eval_path_p2(load_colbert_jina_v2, "jina-colbert-v2 (560M, NC)")),
    # P3: dense retrieve + multi-vector rerank
    ("p3_qwen3-emb-0.6b+answerai-colbert", "P3",
     lambda: eval_path_p3(load_dense_qwen3_emb_06b, load_colbert_answerai_small,
                          "Qwen3-Embedding-0.6B", "answerai-colbert-small-v1")),
    ("p3_qwen3-emb-0.6b+jina-colbert-v2", "P3",
     lambda: eval_path_p3(load_dense_qwen3_emb_06b, load_colbert_jina_v2,
                          "Qwen3-Embedding-0.6B", "jina-colbert-v2 (560M, NC)")),
    ("p3_jina-v5-small+answerai-colbert", "P3",
     lambda: eval_path_p3(load_dense_jina_v5_small, load_colbert_answerai_small,
                          "jina-v5-small", "answerai-colbert-small-v1")),
]


def main():
    all_results = {}
    for label, path_name, runner in CONFIGS:
        try:
            res = runner()
            all_results[label] = {"path": path_name, **res}
        except Exception as e:
            print(f"\n!!! {label} FAILED: {type(e).__name__}: {e}", flush=True)
            all_results[label] = {"path": path_name, "error": f"{type(e).__name__}: {e}"}
        cleanup()

    cats = sorted(data["categories"].keys())
    print("\n\n" + "=" * 100)
    print("=== SUMMARY: Stage 1 — top-1 hits per category, peak co-resident VRAM, mean S1 latency ===")
    print("=" * 100)
    header = f"{'config':50s}  {'path':4s}  " + "  ".join(f"{c[:14]:>14s}" for c in cats)
    header += f"  {'OVERALL':>10s}  {'peak_GB':>8s}  {'mean_ms':>8s}"
    print(header)
    for label, _, _ in CONFIGS:
        res = all_results.get(label, {})
        if "error" in res:
            print(f"{label:50s}  (failed: {res['error'][:50]})")
            continue
        path_name = res.get("path", "?")
        row = []
        total_hits = total = 0
        all_ms = []
        for c in cats:
            rows = res["stage1"].get(c, [])
            hits = sum(1 for _, r, _ in rows if r == 1)
            total_hits += hits
            total += len(rows)
            row.append(f"{hits}/{len(rows)}")
            for _, _, ms in rows:
                all_ms.append(ms)
        mms = sum(all_ms) / len(all_ms) if all_ms else 0
        peak = res.get("co_resident_peak_gb", 0)
        cells = "  ".join(f"{r:>14s}" for r in row)
        print(f"{label:50s}  {path_name:4s}  {cells}  {total_hits}/{total:<7d}  {peak:>7.2f}  {mms:>7.0f}ms")

    print("\n=== SUMMARY: Stage 2 — recall@K per category (correct ranked at-or-above K) ===")
    for k in [1, 5, 10]:
        print(f"\n--- recall@{k} ---")
        header = f"{'config':50s}  " + "  ".join(f"{c[:14]:>14s}" for c in cats) + f"  {'OVERALL':>10s}"
        print(header)
        for label, _, _ in CONFIGS:
            res = all_results.get(label, {})
            if "error" in res:
                print(f"{label:50s}  (failed)")
                continue
            row = []
            total_hits = total = 0
            for c in cats:
                rows = res["stage2"].get(c, [])
                hits = sum(1 for _, r, _ in rows if r <= k)
                total_hits += hits
                total += len(rows)
                row.append(f"{hits}/{len(rows)}")
            cells = "  ".join(f"{r:>14s}" for r in row)
            print(f"{label:50s}  {cells}  {total_hits}/{total}")

    serializable = {}
    for label, res in all_results.items():
        if "error" in res:
            serializable[label] = res
            continue
        clean = {k: v for k, v in res.items() if not isinstance(v, defaultdict)}
        clean["stage1"] = {c: list(rows) for c, rows in res["stage1"].items()}
        clean["stage2"] = {c: list(rows) for c, rows in res["stage2"].items()}
        serializable[label] = clean
    with OUT_FILE.open("w") as f:
        json.dump(serializable, f, indent=2)
    print(f"\nraw results -> {OUT_FILE}")


if __name__ == "__main__":
    main()
