# vllm

[vLLM](https://github.com/vllm-project/vllm) serving [`zeroentropy/zerank-1-small`](https://huggingface.co/zeroentropy/zerank-1-small) as a **score model** for Grudge's RRC reranker stage — a 1.7B Qwen3-based causal LM re-headed to a single-token ("Yes") sequence classifier, scored on `/classify`.

## Build

```sh
docker build -t grudge-vllm containers/vllm
```

A thin wrapper over `vllm/vllm-openai:v0.24.0` (pinned — the score-head recipe below is validated against this version) that pins the model and serving args. Weights are downloaded on first start; vLLM does not cache them into the image. Use `docker compose build vllm` from the repo root to keep build context aligned with the rest of the substrate.

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
- `--hf-overrides '{"architectures":["Qwen3ForSequenceClassification"],"classifier_from_token":["Yes"],"method":"no_post_processing"}'`
- `--max-model-len 2048`
- `--gpu-memory-utilization 0.4`

`--hf-overrides` re-heads the causal LM as a sequence classifier: vLLM auto-resolves `--runner pooling` and `--convert classify` from the architecture (no `--task` flag — v0.24 rejects it). `method: no_post_processing` with `classifier_from_token: ["Yes"]` makes the classifier the single "Yes" row of the `lm_head`, so the last-token pooled logit is the raw "Yes" logit (the `from_2_way_softmax` method is the Qwen-reranker `["no","yes"]` variant, which zerank is not). bf16 puts the model at ~3.4 GB — co-resident with the TEI embedder (~1.2 GB) on a 12 GB consumer GPU with KV + CUDA-graph headroom.

## API

`/classify`, `/score`, `/health` (pooling surface). See [vLLM pooling docs](https://docs.vllm.ai/en/latest/models/pooling_models/scoring/). Note `/v1/chat/completions` is **not** served — this is a pooling model, not a generative one.

## Score-extraction recipe

zerank-1-small reads the raw "Yes" logit at the assistant generation position of a `system=query / user=document` chat prompt (`modeling_zeranker.py`), mapped `sigmoid(logit / 5)`. The Go adapter at `core/adapter/zerank/`:

1. Formats each `(query, document)` as the chat prompt, ending at the assistant turn:

    ```
    <|im_start|>system\n{query}<|im_end|>\n<|im_start|>user\n{document}<|im_end|>\n<|im_start|>assistant\n
    ```

    This is byte-identical to `apply_chat_template(add_generation_prompt=True)`. The prompt is built by the adapter and sent as raw `input`, because vLLM's messages-mode drops the generation prompt (pooling the user-turn end instead of the assistant position).

2. POSTs a batch of prompts to `/classify` with `use_activation: false` (raw logit — vLLM's default sigmoid has no `/5` temperature and saturates in-distribution logits to ~1.0), reading `data[i].probs[0]`.

3. Maps each logit to a probability:

    ```python
    score = 1.0 / (1.0 + math.exp(-logit / 5.0))   # sigmoid(logit / 5.0)
    ```

The `/ 5.0` matches zerank's training setup. Use `score` directly as the rerank value (higher = more relevant).
