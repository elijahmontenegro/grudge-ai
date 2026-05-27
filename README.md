# Spidey

Spidey is an open-source LLM agent framework.

<!-- Drop a screenshot at docs/assets/screenshot.png and uncomment: -->
<!-- ![Spidey UI](docs/assets/screenshot.png) -->

## Memory

Conversation history is stored as-is — every turn, including tool calls
and tool results, in its original format.

When a new turn arrives, prior turns are scored: a bi-encoder picks a
candidate set by embedding similarity; a cross-encoder scores each
candidate against the new turn. A turn is selected when its
cross-encoder score clears three threshold gates — above a raw floor,
above a z-score threshold relative to the candidate batch's mean, and
the batch's own standard deviation must exceed a minimum (if the
cross-encoder can't discriminate this batch, the round returns nothing).

Selected turns are prepended to the LLM input before the new turn, in
the same conversation format they were stored in — same role tags,
same content blocks, same position semantics. There's no
`Here is some relevant context: …` wrapper and no separate context
block; the input to the model is a conversation transcript that's
shorter than the full history.

This technique is documented as Retrieval-Restored Continuation (RRC):
[docs/rrc-paper.md](docs/rrc-paper.md). The paper covers the design
rationale, the threshold gates, reflective re-retrieval mid-generation,
and comparison to RAG, Letta, Zep, FLARE, and Jeong's selective
context reconstruction.

## Architecture

```
core/  <──  service/  ──>  web/
  │           │
  └── rrc/  <─┘
```

- **`core/`** — provider-agnostic interfaces and adapters (Completer,
  Embedder, Scorer, Codec). Adapters wired for Ollama, OpenAI, Anthropic,
  Google AI, vLLM, TEI, zerank.
- **`rrc/`** — the algorithm. Prerequisite detection, selection,
  assembly. No HTTP, no UI; importable on its own.
- **`service/`** — the application. Runs the RRC engine, orchestrates
  ADK-based agents, manages storage, exposes a GraphQL API, serves the
  web UI. One binary.
- **`web/`** — React 19 + Apollo Client. Full RRC introspection —
  selection trace, edges, prerequisite scores, prompt derivation, tool
  timeline.

**Spidey is defined by a formal AsciiDoc spec.** Read
[`docs/spec/MANIFEST.adoc`](docs/spec/MANIFEST.adoc) if the code
disagrees with the docs.

[`ARCHITECTURE.md`](ARCHITECTURE.md) records the design principles the
codebase commits to.

## Quickstart

Requires Go 1.25+, Node 20+, Docker, and [go-task](https://taskfile.dev)
(`brew install go-task` / `winget install Task.Task`).

```bash
task substrate:up    # TEI embedder + vLLM reranker + SearXNG search
task build           # web bundle + Go binary
task run             # serves on http://spidey.localhost:8420
```

`task stop` drains the SQLite WAL cleanly. `task substrate:down` releases
the substrate containers (model weights remain cached in named volumes).

## Model substrate

The default substrate is Apache-2.0 end-to-end:

- **Embedder** — [Qwen3-Embedding-0.6B](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B) via TEI (1024-dim, multilingual).
- **Reranker** — [zerank-1-small](https://huggingface.co/zeroentropy/zerank-1-small) via vLLM (cross-encoder).
- **Generator** — Ollama by default; configure any provider + model in
  Settings.

Co-resident GPU footprint: ~4.6 GB at bf16 — comfortable on a 12 GB
consumer card. Swap any layer for a cloud provider (Anthropic, OpenAI,
Google AI, or a custom vLLM endpoint).

## Status

Pre-1.0. APIs are not yet stable. Issues and PRs are welcome via the
templates in [`.github/`](.github/); see [`CONTRIBUTING.md`](CONTRIBUTING.md)
before opening a PR. Security reports go through
[`SECURITY.md`](SECURITY.md).

## License

Apache 2.0 — see [`LICENSE`](LICENSE).
