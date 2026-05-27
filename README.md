# Spidey

An agent that doesn't lose the plot.

Spidey is an open-source agentic framework built around **Retrieval-Restored
Continuation (RRC)** — a per-step compute-invariant prerequisite-selection
algorithm. The cost per turn stays bounded by retrieval top-K, not by how long
the conversation has been running. Agents that previously degraded as the
corpus grew can keep their footing across thousands of turns.

<!-- Drop a screenshot at docs/assets/screenshot.png and uncomment: -->
<!-- ![Spidey UI](docs/assets/screenshot.png) -->

## Quickstart

Requires Go 1.25+, Node 20+, Docker, and [go-task](https://taskfile.dev)
(`brew install go-task` / `winget install Task.Task`).

```bash
task substrate:up    # TEI embedder + vLLM reranker + SearXNG search
task build           # web bundle + Go binary
task run             # serves on http://spidey.localhost:8420
```

`task stop` drains the SQLite WAL cleanly. `task substrate:down` releases the
substrate containers (model weights remain cached in named volumes).

## Architecture

```
core/  <──  service/  ──>  web/
  │           │
  └── rrc/  <─┘
```

- **`core/`** — provider-agnostic interfaces and adapters (Completer,
  Embedder, Scorer, Codec). Adapters wired for Ollama, OpenAI, Anthropic,
  Google AI, vLLM, TEI, zerank.
- **`rrc/`** — the algorithm. Prerequisite detection, selection, assembly.
  No HTTP, no UI; importable on its own.
- **`service/`** — the application. Runs the RRC engine, orchestrates
  ADK-based agents, manages storage, exposes a GraphQL API, serves the web
  UI. One binary.
- **`web/`** — React 19 + Apollo Client. Full RRC introspection — selection
  trace, edges, prerequisite scores, prompt derivation, tool timeline.

**Spidey is defined by a formal AsciiDoc spec.** Read
[`docs/spec/MANIFEST.adoc`](docs/spec/MANIFEST.adoc) if the code disagrees
with the docs.

[`ARCHITECTURE.md`](ARCHITECTURE.md) records the design principles the
codebase commits to.

## Model substrate

The default substrate is Apache-2.0 end-to-end:

- **Embedder** — [Qwen3-Embedding-0.6B](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B) via TEI (1024-dim, multilingual).
- **Reranker** — [zerank-1-small](https://huggingface.co/zeroentropy/zerank-1-small) via vLLM (cross-encoder).
- **Generator** — Ollama by default; configure any provider + model in
  Settings.

Co-resident GPU footprint: ~4.6 GB at bf16 — comfortable on a 12 GB consumer
card. Swap any layer for a cloud provider (Anthropic, OpenAI, Google AI, or
a custom vLLM endpoint).

## Status

Pre-1.0. APIs are not yet stable. Issues and PRs are welcome via the templates
in [`.github/`](.github/); see [`CONTRIBUTING.md`](CONTRIBUTING.md) before
opening a PR. Security reports go through [`SECURITY.md`](SECURITY.md).

## License

Apache 2.0 — see [`LICENSE`](LICENSE).
