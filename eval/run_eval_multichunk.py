"""Multi-chunk eval — tests whether the right CHUNK of a multi-chunk parent
source surfaces when production-shape chunking is applied first.

Pipeline per (path, candidate combo):
  1. Pre-chunk every source via rrc/chunking.go defaults (2000 char max,
     200 char overlap, paragraph-preferring boundaries).
  2. Pool = all chunks from all sources (~13 chunks across 5 sources).
  3. For each query: rank every chunk; aggregate to source via max-over-chunks.
  4. Report source-level top-1 and recall@K.

This exercises the failure mode the round-2 eval missed: messages > 2000
chars get split into multiple chunks, each indexed separately. The
question is whether the architecture finds the right chunk amongst all
the chunks of the same source PLUS chunks from other sources.

Re-uses model loaders from run_eval_paths.py. Run after that script's
deps are in place (torch+cuda, sentence-transformers, transformers,
pylate, einops, peft, accelerate).
"""
from __future__ import annotations

import gc
import json
import time
from collections import defaultdict
from pathlib import Path

import numpy as np
import torch

import run_eval_paths as harness


PAIRS_FILE = Path(__file__).parent / "multichunk_pairs.json"
OUT_FILE = Path(__file__).parent / "results_multichunk.json"
RERANK_TOP_K = 64


# ============================================================================
# Chunker — port of rrc/chunking.go DefaultChunkConfig (2000 max / 200 overlap
# / paragraph > line > sentence > whitespace boundary preference).
# ============================================================================

def _find_boundary(text: str, start: int, max_end: int, window: int) -> int:
    min_end = max(start, max_end - window)
    if min_end >= max_end:
        return max_end
    seg = text[min_end:max_end]
    # Strongest: paragraph (double newline)
    idx = seg.rfind("\n\n")
    if idx >= 0:
        return min_end + idx + 2
    # Single newline
    idx = seg.rfind("\n")
    if idx >= 0:
        return min_end + idx + 1
    # Sentence terminator + space
    for term in [". ", "! ", "? ", ".\t", "!\t", "?\t"]:
        idx = seg.rfind(term)
        if idx >= 0:
            return min_end + idx + len(term)
    # Whitespace
    idx = max(seg.rfind(" "), seg.rfind("\t"))
    if idx >= 0:
        return min_end + idx + 1
    return max_end


def chunk_text(text: str, max_chars: int = 2000, overlap_chars: int = 200) -> list[str]:
    text = text.strip()
    if not text:
        return []
    if len(text) <= max_chars:
        return [text]
    chunks = []
    i = 0
    while i < len(text):
        end = i + max_chars
        if end >= len(text):
            end = len(text)
        else:
            end = _find_boundary(text, i, end, max_chars // 2)
        piece = text[i:end].strip()
        if piece:
            chunks.append(piece)
        if end >= len(text):
            break
        nxt = end - overlap_chars
        if nxt <= i:
            nxt = i + 1
        i = nxt
    return chunks


# ============================================================================
# Eval pipelines
# ============================================================================

def aggregate_max(chunk_scores: list[float], chunk_to_source: list[str]) -> dict[str, float]:
    """Max score per source across that source's chunks."""
    out: dict[str, float] = {}
    for s, sid in zip(chunk_scores, chunk_to_source):
        if sid not in out or s > out[sid]:
            out[sid] = s
    return out


def rank_sources(chunk_scores: list[float], chunk_to_source: list[str]) -> list[str]:
    src_scores = aggregate_max(chunk_scores, chunk_to_source)
    return sorted(src_scores.keys(), key=lambda k: -src_scores[k])


def eval_p1(layer1_loader, layer2_loader, l1_name, l2_name, sources, pairs):
    print(f"\n=== P1: {l1_name} + {l2_name} ===", flush=True)
    harness.cleanup()
    harness.reset_gpu_stats()

    print(f"  loading L1={l1_name} ...", flush=True)
    t0 = time.time()
    l1_model, embed_q, embed_p = layer1_loader()
    l1_load_s = time.time() - t0
    print(f"    L1 loaded in {l1_load_s:.1f}s, VRAM={harness.gpu_mem_gb():.2f}GB", flush=True)

    # Pre-chunk all sources
    chunk_texts: list[str] = []
    chunk_to_source: list[str] = []
    chunk_intra_idx: list[int] = []
    for sid, text in sources.items():
        cs = chunk_text(text)
        for i, c in enumerate(cs):
            chunk_texts.append(c)
            chunk_to_source.append(sid)
            chunk_intra_idx.append(i)

    print(f"  total chunks across {len(sources)} sources: {len(chunk_texts)}", flush=True)
    print(f"  per-source chunk count: " +
          ", ".join(f"{sid}={chunk_to_source.count(sid)}" for sid in sources), flush=True)

    print(f"  encoding {len(chunk_texts)} chunks (dense) ...", flush=True)
    chunk_embs = embed_p(chunk_texts)

    print(f"  loading L2={l2_name} (co-resident) ...", flush=True)
    t0 = time.time()
    l2_model, score_fn = layer2_loader()
    l2_load_s = time.time() - t0
    co_peak = harness.gpu_peak_gb()
    print(f"    L2 loaded in {l2_load_s:.1f}s. Co-resident peak VRAM={co_peak:.2f}GB", flush=True)

    results = {"per_pair": [], "co_resident_peak_gb": co_peak,
               "l1_load_s": l1_load_s, "l2_load_s": l2_load_s,
               "total_chunks": len(chunk_texts)}

    for p in pairs:
        qid = p["id"]; correct_sid = p["correct_source_id"]
        t0 = time.time()
        q_emb = embed_q([p["query"]])[0]
        l1_scores = chunk_embs @ q_emb
        topk_idx = np.argsort(-l1_scores)[:RERANK_TOP_K].tolist()
        topk_texts = [chunk_texts[i] for i in topk_idx]
        l2_scores = score_fn(p["query"], topk_texts)
        # Build full chunk-score vector: top-K get reranker score, rest get a low fallback
        full_scores = [-1e9] * len(chunk_texts)
        for idx, s in zip(topk_idx, l2_scores):
            full_scores[idx] = s
        ranking = rank_sources(full_scores, chunk_to_source)
        rank = ranking.index(correct_sid) + 1
        ms = (time.time() - t0) * 1000
        results["per_pair"].append((qid, correct_sid, rank, ms))
        print(f"    {qid:25s} correct={correct_sid:30s} source-rank=#{rank}/{len(sources)}  {ms:.0f}ms",
              flush=True)

    del l1_model, l2_model, embed_q, embed_p, score_fn
    harness.cleanup()
    return results


def eval_p2(loader, name, sources, pairs):
    print(f"\n=== P2: {name} (single-stage) ===", flush=True)
    harness.cleanup()
    harness.reset_gpu_stats()

    print(f"  loading {name} ...", flush=True)
    t0 = time.time()
    model, encode_q, encode_p, maxsim = loader()
    load_s = time.time() - t0
    peak = harness.gpu_peak_gb()
    print(f"    loaded in {load_s:.1f}s, peak VRAM={peak:.2f}GB", flush=True)

    chunk_texts: list[str] = []
    chunk_to_source: list[str] = []
    for sid, text in sources.items():
        for c in chunk_text(text):
            chunk_texts.append(c)
            chunk_to_source.append(sid)
    print(f"  total chunks: {len(chunk_texts)}", flush=True)

    print(f"  encoding chunks (multi-vector) ...", flush=True)
    chunk_mvs = encode_p(chunk_texts)

    results = {"per_pair": [], "co_resident_peak_gb": peak, "load_s": load_s,
               "total_chunks": len(chunk_texts)}

    for p in pairs:
        qid = p["id"]; correct_sid = p["correct_source_id"]
        t0 = time.time()
        q_mv = encode_q([p["query"]])[0]
        scores = maxsim(q_mv, list(chunk_mvs))
        ranking = rank_sources(scores, chunk_to_source)
        rank = ranking.index(correct_sid) + 1
        ms = (time.time() - t0) * 1000
        results["per_pair"].append((qid, correct_sid, rank, ms))
        print(f"    {qid:25s} correct={correct_sid:30s} source-rank=#{rank}/{len(sources)}  {ms:.0f}ms",
              flush=True)

    del model, encode_q, encode_p, maxsim
    harness.cleanup()
    return results


def eval_p3(layer1_loader, mv_loader, l1_name, mv_name, sources, pairs):
    print(f"\n=== P3: {l1_name} + {mv_name} (cascaded) ===", flush=True)
    harness.cleanup()
    harness.reset_gpu_stats()

    print(f"  loading L1={l1_name} ...", flush=True)
    t0 = time.time()
    l1_model, embed_q, embed_p = layer1_loader()
    l1_load_s = time.time() - t0

    chunk_texts: list[str] = []
    chunk_to_source: list[str] = []
    for sid, text in sources.items():
        for c in chunk_text(text):
            chunk_texts.append(c)
            chunk_to_source.append(sid)

    print(f"  encoding {len(chunk_texts)} chunks (dense) ...", flush=True)
    chunk_embs = embed_p(chunk_texts)

    print(f"  loading L2={mv_name} (co-resident) ...", flush=True)
    t0 = time.time()
    mv_model, mv_encode_q, mv_encode_p, mv_maxsim = mv_loader()
    l2_load_s = time.time() - t0
    co_peak = harness.gpu_peak_gb()
    print(f"    L2 loaded in {l2_load_s:.1f}s. Co-resident peak={co_peak:.2f}GB", flush=True)

    results = {"per_pair": [], "co_resident_peak_gb": co_peak,
               "l1_load_s": l1_load_s, "l2_load_s": l2_load_s,
               "total_chunks": len(chunk_texts)}

    for p in pairs:
        qid = p["id"]; correct_sid = p["correct_source_id"]
        t0 = time.time()
        q_emb = embed_q([p["query"]])[0]
        l1_scores = chunk_embs @ q_emb
        topk_idx = np.argsort(-l1_scores)[:RERANK_TOP_K].tolist()
        topk_texts = [chunk_texts[i] for i in topk_idx]
        q_mv = mv_encode_q([p["query"]])[0]
        topk_mvs = mv_encode_p(topk_texts)
        topk_scores = mv_maxsim(q_mv, list(topk_mvs))
        full_scores = [-1e9] * len(chunk_texts)
        for idx, s in zip(topk_idx, topk_scores):
            full_scores[idx] = s
        ranking = rank_sources(full_scores, chunk_to_source)
        rank = ranking.index(correct_sid) + 1
        ms = (time.time() - t0) * 1000
        results["per_pair"].append((qid, correct_sid, rank, ms))
        print(f"    {qid:25s} correct={correct_sid:30s} source-rank=#{rank}/{len(sources)}  {ms:.0f}ms",
              flush=True)

    del l1_model, mv_model, embed_q, embed_p, mv_encode_q, mv_encode_p, mv_maxsim
    harness.cleanup()
    return results


# ============================================================================
# Driver
# ============================================================================

def main():
    data = json.loads(PAIRS_FILE.read_text(encoding="utf-8"))
    sources = data["sources"]
    pairs = data["pairs"]

    configs = [
        # P1
        ("p1_qwen3-emb-0.6b+qwen3-rerank-0.6b", "P1",
         lambda: eval_p1(harness.load_dense_qwen3_emb_06b, harness.load_rerank_qwen3_06b,
                         "Qwen3-Embedding-0.6B", "Qwen3-Reranker-0.6B", sources, pairs)),
        ("p1_qwen3-emb-0.6b+zerank-1-small", "P1",
         lambda: eval_p1(harness.load_dense_qwen3_emb_06b, harness.load_rerank_zerank_1_small,
                         "Qwen3-Embedding-0.6B", "zerank-1-small", sources, pairs)),
        ("p1_jina-v5-small+zerank-1-small", "P1",
         lambda: eval_p1(harness.load_dense_jina_v5_small, harness.load_rerank_zerank_1_small,
                         "jina-v5-small", "zerank-1-small", sources, pairs)),
        # P2
        ("p2_answerai-colbert-small", "P2",
         lambda: eval_p2(harness.load_colbert_answerai_small, "answerai-colbert-small-v1", sources, pairs)),
        ("p2_gte-moderncolbert-v1", "P2",
         lambda: eval_p2(harness.load_colbert_gte_modernbert, "GTE-ModernColBERT-v1", sources, pairs)),
        ("p2_jina-colbert-v2", "P2",
         lambda: eval_p2(harness.load_colbert_jina_v2, "jina-colbert-v2 (560M, NC)", sources, pairs)),
        # P3
        ("p3_qwen3-emb-0.6b+answerai-colbert", "P3",
         lambda: eval_p3(harness.load_dense_qwen3_emb_06b, harness.load_colbert_answerai_small,
                         "Qwen3-Embedding-0.6B", "answerai-colbert-small-v1", sources, pairs)),
        ("p3_qwen3-emb-0.6b+jina-colbert-v2", "P3",
         lambda: eval_p3(harness.load_dense_qwen3_emb_06b, harness.load_colbert_jina_v2,
                         "Qwen3-Embedding-0.6B", "jina-colbert-v2 (560M, NC)", sources, pairs)),
    ]

    all_results = {}
    for label, path_name, runner in configs:
        try:
            res = runner()
            all_results[label] = {"path": path_name, **res}
        except Exception as e:
            print(f"\n!!! {label} FAILED: {type(e).__name__}: {e}", flush=True)
            all_results[label] = {"path": path_name, "error": f"{type(e).__name__}: {e}"}
        harness.cleanup()

    print("\n\n" + "=" * 100)
    print("=== MULTICHUNK SUMMARY: source-level rank for the right source out of "
          f"{len(sources)} sources ===")
    print("=" * 100)
    header = f"{'config':50s}  {'path':4s}  " + "  ".join(f"{p['id'][:18]:>18s}" for p in pairs)
    header += f"  {'top1':>6s}  {'r@2':>6s}  {'r@3':>6s}  {'peak_GB':>8s}  {'mean_ms':>8s}"
    print(header)
    for label, _, _ in configs:
        res = all_results.get(label, {})
        if "error" in res:
            print(f"{label:50s}  FAILED")
            continue
        path = res.get("path", "?")
        ranks_per_pair = {pp[0]: pp[2] for pp in res.get("per_pair", [])}
        ms_per_pair = [pp[3] for pp in res.get("per_pair", [])]
        cells = "  ".join(f"#{ranks_per_pair.get(p['id'], 'X'):>16}" for p in pairs)
        ranks = list(ranks_per_pair.values())
        top1 = sum(1 for r in ranks if r == 1)
        r2 = sum(1 for r in ranks if r <= 2)
        r3 = sum(1 for r in ranks if r <= 3)
        peak = res.get("co_resident_peak_gb", 0)
        mean_ms = sum(ms_per_pair) / len(ms_per_pair) if ms_per_pair else 0
        print(f"{label:50s}  {path:4s}  {cells}  {top1}/{len(pairs):<4d}  "
              f"{r2}/{len(pairs):<4d}  {r3}/{len(pairs):<4d}  {peak:>7.2f}  {mean_ms:>7.0f}ms")

    OUT_FILE.write_text(json.dumps(all_results, indent=2), encoding="utf-8")
    print(f"\nraw results -> {OUT_FILE}")


if __name__ == "__main__":
    main()
