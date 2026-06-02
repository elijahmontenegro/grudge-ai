# vllm

[vLLM](https://github.com/vllm-project/vllm) serving [`zeroentropy/zerank-1-small`](https://huggingface.co/zeroentropy/zerank-1-small) over the OpenAI-compatible API. Used as Grudge's RRC reranker stage — a cross-encoder repurposed from a 1.7B Qwen3-based causal LM via the "Yes" token logprob recipe.

## Build

```sh
docker build -t grudge-vllm containers/vllm
```

A thin wrapper over `vllm/vllm-openai:latest` that pins the model and serving args. Weights are downloaded on first start; vLLM does not cache them into the image. Use `docker compose build vllm` from the repo root to keep build context aligned with the rest of the substrate.

## Run

`docker compose up -d vllm` from the repo root binds the HF cache to a named volume so weights persist. The standalone form, for reference:

```sh
docker run --gpus all -p 8000:8000 \
  -v ${HF_HOME:-$HOME/.cache/huggingface}:/root/.cache/huggingface \
  grudge-vllm
```

## Serving args

Set in `Dockerfile` `CMD`:

- `--model zeroentropy/zerank-1-small`
- `--port 8000`
- `--dtype bfloat16`
- `--enable-prefix-caching`
- `--max-model-len 2048`
- `--gpu-memory-utilization 0.4`
- `--quantization bitsandbytes` (load-format matches)

bf16 puts the model at ~3.4 GB. Co-resident with the TEI embedder (~1.2 GB) leaves headroom on a 12 GB consumer GPU for KV cache + CUDA graphs.

## API

Standard vLLM OpenAI-compatible surface — `/v1/chat/completions`, `/v1/completions`, `/health`. See [vLLM docs](https://docs.vllm.ai/en/latest/serving/openai_compatible_server.html).

## Score-extraction recipe

zerank-1-small is a cross-encoder framed as a chat model: it answers a relevance question with a single token (`Yes` / `No`). The Go adapter at `core/adapter/zerank/` extracts the score:

1. Send the (query, document) pair as a chat-completions request with `max_tokens=1`, `logprobs=true`, `top_logprobs=20`.
2. From `choices[0].logprobs.content[0].top_logprobs`, find the entry whose token is `Yes` and read its `logprob`.
3. Apply temperature scaling to map the logit to a probability:

    ```python
    score = 1.0 / (1.0 + math.exp(-logprob / 5.0))   # sigmoid(logit / 5.0)
    ```

The `/ 5.0` is calibrated to match zerank's training setup. Use `score` directly as the rerank value (higher = more relevant).
