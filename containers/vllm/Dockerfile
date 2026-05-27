FROM vllm/vllm-openai:latest

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=5s --start-period=120s --retries=3 \
    CMD curl -fsS http://localhost:8000/health || exit 1

# vllm/vllm-openai's entrypoint is `python3 -m vllm.entrypoints.openai.api_server`,
# so CMD here supplies only the args. zerank-1-small is a 1.7B Qwen3-based causal
# LM repurposed as a reranker via a chat-template + last-position "Yes" token
# logit + sigmoid(logit/5) recipe; the adapter (core/adapter/zerank/) is
# identical-recipe to the one used for the larger zerank-2 — only the model
# identity changes. bf16 (no quantization); total ~3.4GB at this size, fits
# comfortably alongside the embedder service (Qwen3-Embedding-0.6B at ~1.2GB)
# on a 12GB GPU with KV cache + CUDA graph headroom.
CMD ["--model", "zeroentropy/zerank-1-small", \
     "--port", "8000", \
     "--dtype", "bfloat16", \
     "--enable-prefix-caching", \
     "--max-model-len", "2048", \
     "--gpu-memory-utilization", "0.4", \
     "--quantization", "bitsandbytes", \
     "--load-format", "bitsandbytes"]
