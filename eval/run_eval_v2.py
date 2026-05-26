"""25-pair discrimination eval across four cross-encoder rerankers.

Each model uses its own documented standard inference path:
  - bge-reranker-v2-m3      → sentence-transformers CrossEncoder (matches TEI's pattern)
  - mxbai-rerank-large-v2   → sentence-transformers CrossEncoder
  - zerank-2                → sentence-transformers CrossEncoder (Qwen3-4B base, pad_token fix)
  - Qwen3-Reranker-4B       → transformers AutoModelForCausalLM, yes/no logit extraction
                              (the official Qwen recipe; CrossEncoder.predict() doesn't
                              apply for causal-LM rerankers)

All models loaded sequentially to fit 12GB GPU. Each test pair is one query
against {1 correct, 4 distractors}. Top-1 hit = correct candidate ranked #1.
"""
import json, math, time, gc
from collections import defaultdict
from pathlib import Path

import torch

PAIRS_FILE = Path(__file__).parent / "pairs.json"
with PAIRS_FILE.open() as f:
    data = json.load(f)

def zscore(v, arr):
    n = len(arr); m = sum(arr)/n
    var = sum((x-m)**2 for x in arr)/n
    sd = math.sqrt(var) if var > 0 else 1e-12
    return (v - m) / sd

# === Per-model scoring functions ===

def score_cross_encoder(hf_id):
    """Plain sentence-transformers CrossEncoder loader for models that
    actually train a classification head against the standard interface
    (e.g., bge-reranker-v2-m3)."""
    from sentence_transformers import CrossEncoder
    model = CrossEncoder(hf_id, trust_remote_code=True, device="cuda",
                         model_kwargs={"torch_dtype": torch.bfloat16})
    def score(query, candidates):
        pairs = [(query, c) for c in candidates]
        out = model.predict(pairs, show_progress_bar=False)
        return [float(s) for s in (out if hasattr(out, "__iter__") else [out])]
    return model, score

def score_zerank(hf_id):
    """zerank-2 ships modeling_zeranker.py with a CrossEncoder.predict
    monkey-patch that doesn't trigger via current sentence-transformers'
    trust_remote_code path. Replicating their inference directly:
    AutoModelForCausalLM + chat template (system=query, user=doc) +
    last-position yes-token logit + sigmoid(logit/5)."""
    from transformers import AutoModelForCausalLM, AutoTokenizer
    tok = AutoTokenizer.from_pretrained(hf_id, padding_side="right")
    if tok.pad_token is None:
        tok.pad_token = tok.eos_token
    model = AutoModelForCausalLM.from_pretrained(hf_id, torch_dtype="auto", device_map="cuda")
    model.eval()
    yes_id = tok.encode("Yes", add_special_tokens=False)[0]

    def score(query, candidates):
        out = []
        for c in candidates:
            messages = [
                {"role": "system", "content": query.strip()},
                {"role": "user", "content": c.strip()},
            ]
            text = tok.apply_chat_template(messages, tokenize=False, add_generation_prompt=True)
            inputs = tok(text, return_tensors="pt", truncation=True, max_length=8192).to("cuda")
            with torch.no_grad():
                logits = model(**inputs, use_cache=False).logits
            attention_mask = inputs.attention_mask
            last_pos = attention_mask.sum(dim=1) - 1
            last_logits = logits[0, last_pos[0]]
            yes_logit = float(last_logits[yes_id]) / 5.0
            score_val = 1.0 / (1.0 + math.exp(-yes_logit))
            out.append(score_val)
        return out
    return model, score

def score_mxbai_v2(hf_id):
    """Mixedbread's MxbaiRerankV2 class — proper inference for v2 (which
    is RL-trained and doesn't load correctly via plain CrossEncoder)."""
    from mxbai_rerank import MxbaiRerankV2
    model = MxbaiRerankV2(model_name_or_path=hf_id, device="cuda", torch_dtype=torch.bfloat16)
    def score(query, candidates):
        results = model.rank(query, candidates, top_k=len(candidates), sort=False, return_documents=False)
        # results is a list of RankResult objects; align by index field
        out = [0.0] * len(candidates)
        for r in results:
            out[r.index] = float(r.score)
        return out
    return model, score

def score_qwen3_reranker(hf_id):
    """Official Qwen3-Reranker recipe: format as prompt, extract yes/no logits."""
    from transformers import AutoTokenizer, AutoModelForCausalLM
    tok = AutoTokenizer.from_pretrained(hf_id, padding_side="left")
    model = AutoModelForCausalLM.from_pretrained(hf_id, torch_dtype=torch.bfloat16, device_map="cuda")
    model.eval()
    if tok.pad_token is None:
        tok.pad_token = tok.eos_token

    # Per Qwen3-Reranker model card recipe
    prefix = (
        "<|im_start|>system\nJudge whether the Document meets the requirements based on "
        "the Query and the Instruct provided. Note that the answer can only be \"yes\" or "
        "\"no\".<|im_end|>\n<|im_start|>user\n"
    )
    suffix = "<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n"
    instruction = ("Given a query, determine whether the document contains the prerequisite "
                   "context required for the query to be coherently answered.")

    yes_token_id = tok("yes", add_special_tokens=False).input_ids[0]
    no_token_id = tok("no", add_special_tokens=False).input_ids[0]

    def score(query, candidates):
        out = []
        for c in candidates:
            body = (f"<Instruct>: {instruction}\n<Query>: {query}\n<Document>: {c}")
            prompt = prefix + body + suffix
            inputs = tok(prompt, return_tensors="pt", truncation=True, max_length=8192).to("cuda")
            with torch.no_grad():
                logits = model(**inputs).logits[0, -1, :]
            yes_logit = logits[yes_token_id].item()
            no_logit = logits[no_token_id].item()
            # softmax over just yes/no for relevance probability
            mx = max(yes_logit, no_logit)
            yes_p = math.exp(yes_logit - mx)
            no_p = math.exp(no_logit - mx)
            out.append(yes_p / (yes_p + no_p))
        return out
    return model, score

# === Model list ===
MODELS = [
    ("bge-m3",   "BAAI/bge-reranker-v2-m3",                lambda: score_cross_encoder("BAAI/bge-reranker-v2-m3")),
    ("mxbai-v2", "mixedbread-ai/mxbai-rerank-large-v2",    lambda: score_mxbai_v2("mixedbread-ai/mxbai-rerank-large-v2")),
    ("zerank-2", "zeroentropy/zerank-2",                   lambda: score_zerank("zeroentropy/zerank-2")),
    ("qwen3-4b", "Qwen/Qwen3-Reranker-4B",                 lambda: score_qwen3_reranker("Qwen/Qwen3-Reranker-4B")),
]

results = {short: defaultdict(list) for short, _, _ in MODELS}

for short, hf_id, loader in MODELS:
    print(f"\n=== {short} ({hf_id}) ===", flush=True)
    print("loading...", flush=True)
    t0 = time.time()
    try:
        model, score = loader()
    except Exception as e:
        print(f"LOAD FAILED: {type(e).__name__}: {e}", flush=True)
        continue
    print(f"loaded in {time.time()-t0:.1f}s, mem={torch.cuda.memory_allocated()/1e9:.2f}GB", flush=True)

    for cat, pairs in data["categories"].items():
        for p in pairs:
            cands = [p["correct"]] + p["distractors"]
            t0 = time.time()
            try:
                scores = score(p["query"], cands)
            except Exception as e:
                print(f"  {cat[:3]} {p['id']:18s} FAIL: {type(e).__name__}: {str(e)[:80]}", flush=True)
                results[short][cat].append((p["id"], None, None, None))
                continue
            ranking = sorted(range(len(cands)), key=lambda i: -scores[i])
            rank_correct = ranking.index(0) + 1
            z = zscore(scores[0], scores)
            ms = (time.time() - t0) * 1000
            top1 = cands[ranking[0]][:32]
            results[short][cat].append((p["id"], rank_correct, z, ms))
            print(f"  {cat[:3]} {p['id']:18s} rank={rank_correct} z={z:+.2f} {ms:6.0f}ms  top1={top1}", flush=True)

    del model
    if "score" in dir(): del score
    gc.collect()
    torch.cuda.empty_cache()
    torch.cuda.synchronize()
    print(f"unloaded, mem={torch.cuda.memory_allocated()/1e9:.2f}GB", flush=True)

# === Summary ===
print("\n\n=== Summary: top-1 hit rate per category ===", flush=True)
cats = sorted(data["categories"].keys())
print(f"{'model':10s}  " + "  ".join(f"{c[:14]:>14s}" for c in cats) + f"  {'OVERALL':>10s}  {'mean_z':>7s}  {'mean_ms':>9s}")
for short, _, _ in MODELS:
    row = []
    total_hits = total = 0
    all_z = []
    all_ms = []
    for c in cats:
        rows = results[short].get(c, [])
        n = len(rows)
        hits = sum(1 for _, r, _, _ in rows if r == 1)
        total_hits += hits
        total += n
        row.append(f"{hits}/{n}")
        for _, _, z, ms in rows:
            if z is not None: all_z.append(z)
            if ms is not None: all_ms.append(ms)
    mz = sum(all_z)/len(all_z) if all_z else 0
    mms = sum(all_ms)/len(all_ms) if all_ms else 0
    cell_str = "  ".join(f"{r:>14s}" for r in row)
    print(f"{short:10s}  {cell_str}  {total_hits}/{total:<7d}  {mz:+7.2f}  {mms:>8.0f}ms")

out_path = Path(__file__).parent / "results_v2.json"
with out_path.open("w") as f:
    json.dump({m: {c: list(rows) for c, rows in cats_dict.items()} for m, cats_dict in results.items()}, f, indent=2)
print(f"\nraw results -> {out_path}")
