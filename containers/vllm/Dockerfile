# Pinned, not :latest — the serving recipe below (hf-overrides re-head, pooler
# semantics) is validated against this vLLM version; a silent base bump could
# change classifier_from_token / no_post_processing behaviour.
FROM vllm/vllm-openai:v0.24.0

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=5s --start-period=120s --retries=3 \
    CMD curl -fsS http://localhost:8000/health || exit 1

# vllm/vllm-openai's entrypoint is `python3 -m vllm.entrypoints.openai.api_server`,
# so CMD here supplies only the args. zerank-1-small is a 1.7B Qwen3-based causal
# LM repurposed as a reranker: modeling_zeranker.py reads the raw "Yes" logit at
# the assistant generation position of a system=query/user=document chat prompt,
# score = sigmoid(logit/5). It is served as a SCORE model, not a generative one:
#   - architectures -> Qwen3ForSequenceClassification re-heads the causal LM as a
#     pooling/classify model (vLLM auto-resolves --runner pooling, --convert
#     classify from this; no --task flag, which v0.24 does not accept).
#   - method "no_post_processing" + classifier_from_token ["Yes"] makes the
#     classifier the single "Yes" row of the lm_head, so the LAST-token pooled
#     logit IS that raw "Yes" logit (from_2_way_softmax is the Qwen-reranker
#     no/yes variant, which zerank is not).
# The adapter (core/adapter/zerank) posts the chat prompt to /classify with
# use_activation=false to read that raw logit and applies sigmoid(logit/5) — vLLM's
# built-in activation is a plain sigmoid whose missing /5 saturates every score to
# ~1.0. The old generative chat-completions + "Yes"-logprob recipe could not read a
# pre-softmax logit off the OpenAI API and derailed into the model's <think>
# prelude. bf16, no quantization: ~3.4GB, co-resident with Qwen3-Embedding-0.6B
# (~1.2GB) on a 12GB GPU with KV + CUDA-graph headroom.
CMD ["--model", "zeroentropy/zerank-1-small", \
     "--port", "8000", \
     "--dtype", "bfloat16", \
     "--hf-overrides", "{\"architectures\": [\"Qwen3ForSequenceClassification\"], \"classifier_from_token\": [\"Yes\"], \"method\": \"no_post_processing\"}", \
     "--max-model-len", "2048", \
     "--gpu-memory-utilization", "0.4"]
