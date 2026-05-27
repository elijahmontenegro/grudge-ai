FROM vllm/vllm-openai:latest

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=5s --start-period=120s --retries=3 \
    CMD curl -fsS http://localhost:8000/health || exit 1

# vllm/vllm-openai's entrypoint is `python3 -m vllm.entrypoints.openai.api_server`,
# so CMD here supplies only the args. zerank-1-small is a 1.7B Qwen3-based causal
# LM repurposed as a reranker via the same chat-template + last-position "Yes"
# token logit + sigmoid(logit/5) recipe as the larger zerank-2 — adapter code is
# identical-recipe; only the model identity changes. bf16 (no quantization);
# total ~3.4GB at this size, fits comfortably alongside the embedder service
# (Qwen3-Embedding-0.6B at ~1.2GB) on a 12GB GPU with KV cache + CUDA graph
# headroom. The bitsandbytes + reduced --gpu-memory-utilization workaround from
# the prior session was specific to fitting zerank-2 (4B) alongside zembed-1
# (4B) — that pair was the eval winner but didn't fit at any precision; this
# pair is the new principled choice (Apache 2.0, ties NC pair on quality, fits
# at full bf16).
CMD ["--model", "zeroentropy/zerank-1-small", \
     "--port", "8000", \
     "--dtype", "bfloat16", \
     "--enable-prefix-caching", \
     "--max-model-len", "2048", \
     "--gpu-memory-utilization", "0.4", \
     "--quantization", "bitsandbytes", \
     "--load-format", "bitsandbytes"]
