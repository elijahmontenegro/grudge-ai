# vllm

[vLLM](https://github.com/vllm-project/vllm) serving [`zeroentropy/zerank-2`](https://huggingface.co/zeroentropy/zerank-2) over the OpenAI-compatible API. Used by Spidey's RRC reranker stage as a cross-encoder.

## Build

```sh
docker build -t spidey-vllm .
```

This is a thin wrapper over `vllm/vllm-openai:latest` that pins the model and serving args. Weights are downloaded on first start (vLLM does not cache into the image).

## Run

```sh
docker run --gpus all -p 8000:8000 \
  -v ${HF_HOME:-$HOME/.cache/huggingface}:/root/.cache/huggingface \
  spidey-vllm
```

Mount the HF cache so weights persist across container restarts.

Serving args (set in `Dockerfile` `CMD`):

- `--model zeroentropy/zerank-2`
- `--port 8000`
- `--enable-prefix-caching`
- `--dtype bfloat16`
- `--max-model-len 8192`

## API

Standard vLLM OpenAI-compatible surface — `/v1/chat/completions`, `/v1/completions`, `/health`, etc. See [vLLM docs](https://docs.vllm.ai/en/latest/serving/openai_compatible_server.html).

## Score-extraction recipe

zerank-2 is a cross-encoder framed as a chat model: it answers a relevance question with a single token (`Yes` / `No`). Score extraction:

1. Send the query/document pair as a chat-completions request that ends with the assistant about to reply.
2. Request `logprobs=true` and `top_logprobs >= 5` so the `Yes` candidate is in the top set.
3. From `choices[0].logprobs.content[0].top_logprobs`, find the entry whose token is `Yes` and read its `logprob`.
4. Apply temperature scaling to map the logit to a probability:

    ```python
    score = 1.0 / (1.0 + math.exp(-logprob / 5.0))   # sigmoid(logit / 5.0)
    ```

The `/ 5.0` is calibrated to match zerank's training setup. Use `score` directly as the rerank value (higher = more relevant).
