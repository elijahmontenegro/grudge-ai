# Spidey

An RRC-native, provider-agnostic agentic framework. One binary, served on
`spidey.localhost:8420`. Built around **Retrieval-Restored Continuation** —
a per-step compute-invariant prerequisite-selection algorithm that lets an
agent operate over a corpus that grows without bound while keeping the
prompt budget fixed.

## What it does

- Runs a tool-using agent (Google ADK orchestration) against any provider
  that implements the `core.Completer` / `core.Embedder` / `core.Classifier`
  interfaces. Ollama, OpenAI, Anthropic, GoogleAI, vLLM, TEI, zerank are
  wired in `core/adapter/`.
- Uses **RRC** to pick prerequisite messages out of arbitrary corpus size
  per turn. Cost per step stays bounded by retrieval top-K, not by corpus
  length.
- Persists messages, chunks, embeddings, scores, edges, selections, agent
  state, and per-tick latency traces in SQLite (with sqlite-vec for ANN).
- Serves a React web UI on the same port, including full introspection of
  what RRC selected, excluded, and scored.

## Architecture

Four discrete pieces, four specs (`spec/`):

```
gen/go  <──  core  <──  service  ──>  web
  │                       │
  ├────── rrc   <─────────┘
```

- **`core/`** — provider-agnostic interfaces + adapters (Completer,
  Embedder, Classifier, Codec, retry policy).
- **`rrc/`** — the algorithm. Prerequisite detection, selection,
  assembly. Stateful when given persistent storage, stateless when called
  per request. No HTTP, no UI.
- **`service/`** — the application. Runs the RRC engine, orchestrates
  ADK agents, manages storage, exposes the GraphQL API, serves the web
  UI. One binary.
- **`web/`** — React 19 + Apollo Client. Speaks GraphQL to the service.
  Full RRC introspection: selection trace, edges, prerequisite scores,
  prompt derivation, agent state, tool execution timeline.

The protocol specs are the truth (`spec/MANIFEST.adoc`, `spec/core/`,
`spec/rrc/`, `spec/service/`, `spec/web/`). Read those if the code
disagrees with the docs.

## Getting started

Requires Go 1.25+, Docker (for the inference substrate), Node 20+ (for
the web build).

```bash
# 1. Bring up the inference substrate (TEI embedder, vLLM reranker, SearXNG)
task substrate:up

# 2. Build the web bundle and the Go binary
task build

# 3. Run
task run

# Open the UI
# http://spidey.localhost:8420
```

`task stop` shuts down spidey gracefully (graceful WAL drain). `task
substrate:down` brings down the substrate containers.

## Model substrate

The default substrate is Apache-2.0 across the board:

- **Embedder**: [Qwen3-Embedding-0.6B](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B)
  via TEI. 1024-dim outputs, multilingual.
- **Reranker**: [zerank-1-small](https://huggingface.co/zeroentropy/zerank-1-small)
  via vLLM. Cross-encoder for prerequisite scoring.
- **Generator**: Ollama by default (any compatible model — GLM, Qwen,
  Llama, etc.). Configure provider + model via Settings.

Co-resident GPU footprint: ~4.6 GB at bf16. Fits comfortably on a 12 GB
consumer card.

You can swap any of these for a cloud provider — Anthropic, OpenAI,
Google AI, and a custom vLLM endpoint are wired adapters.

## Project layout

```
core/                provider-agnostic interfaces + adapters
rrc/                 the algorithm (prerequisite detection + selection)
service/             the application binary (kernel, agent, storage, GraphQL)
gen/go/              proto-generated Go types
scripts/             verifydeps + utility scripts
templates/           runtime prompt templates (loaded by service)
web/                 React 19 + Apollo Client UI
spec/                AsciiDoc protocol specs (the source of truth)
docs/eval-reports/   substrate eval methodology + results
proto/               .proto sources
containers/          Dockerfiles for the inference substrate
infra/               substrate-adjacent infra config (SearXNG)
testdata/            shared test fixtures
```

## License

Apache 2.0 — see [`LICENSE`](LICENSE).

Dependencies and the default model substrate are also Apache-2.0
(Google ADK, Qwen3-Embedding-0.6B, zerank-1-small).
