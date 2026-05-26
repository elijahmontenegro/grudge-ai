"""Subset rerun for jina-colbert-v2 configs that failed on einops missing.

Loads only the two configs that need jina-colbert-v2 and reruns them. Appends
results into results_paths.json.
"""
import json
from pathlib import Path

import run_eval_paths as harness

OUT = Path(__file__).parent / "results_paths.json"

CONFIGS = [
    ("p2_jina-colbert-v2", "P2",
     lambda: harness.eval_path_p2(harness.load_colbert_jina_v2,
                                  "jina-colbert-v2 (560M, NC)")),
    ("p3_qwen3-emb-0.6b+jina-colbert-v2", "P3",
     lambda: harness.eval_path_p3(harness.load_dense_qwen3_emb_06b,
                                  harness.load_colbert_jina_v2,
                                  "Qwen3-Embedding-0.6B",
                                  "jina-colbert-v2 (560M, NC)")),
]

if OUT.exists():
    existing = json.loads(OUT.read_text(encoding="utf-8"))
else:
    existing = {}

for label, path_name, runner in CONFIGS:
    try:
        res = runner()
    except Exception as e:
        print(f"\n!!! {label} FAILED: {type(e).__name__}: {e}", flush=True)
        existing[label] = {"path": path_name, "error": f"{type(e).__name__}: {e}"}
        harness.cleanup()
        continue

    clean = {k: v for k, v in res.items() if hasattr(v, "items") and not k.startswith("stage")}
    clean["path"] = path_name
    clean["stage1"] = {c: list(rows) for c, rows in res["stage1"].items()}
    clean["stage2"] = {c: list(rows) for c, rows in res["stage2"].items()}
    for k in ("co_resident_peak_gb", "l1_mem_gb", "l1_load_s", "l2_load_s", "load_s"):
        if k in res:
            clean[k] = res[k]
    existing[label] = clean
    harness.cleanup()

OUT.write_text(json.dumps(existing, indent=2), encoding="utf-8")
print(f"\nresults appended -> {OUT}")
