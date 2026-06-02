# Grudge

[![CI](https://github.com/elijahmontenegro/grudge-ai/actions/workflows/ci.yml/badge.svg)](https://github.com/elijahmontenegro/grudge-ai/actions/workflows/ci.yml)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/elijahmontenegro/grudge-ai)](go.mod)

Grudge is an open-source, local-first LLM agent framework.

A single Go binary runs in the system tray and serves the agentic
terminal to your browser at `grudge.localhost`. Electron without the
Electron. No bundled Chromium. Pragmatic, extensible, light.

<!-- Drop a screenshot at docs/assets/screenshot.png and uncomment: -->
<!-- ![Grudge UI](docs/assets/screenshot.png) -->

## Memory

LLMs are stateless. To sustain a conversation, the orchestrator sends
prior messages on every call. Sending the whole history runs into
context-window limits and context rot.

Grudge stores conversation history losslessly — every message,
including tool calls and tool results, in its original format. On
every turn, the orchestrator finds the prior messages the new message
depends on and includes only those, restored as plain conversation
turns indistinguishable from the new turn. The model receives a
coherent, bounded input regardless of how long the stored history has
grown. There's no separate "session"; the stored history is the
conversation.

This technique is Retrieval-Restored Continuation (RRC). A paper
formalising it is in preparation, with comparisons to RAG, Letta, Zep,
FLARE, and Jeong's selective context reconstruction. How Grudge
implements it lives at `docs/spec/rrc/MANIFEST.adoc`.

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

**Grudge is defined by a formal AsciiDoc spec.** Read
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
task run             # serves on http://grudge.localhost:8420
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

GNU Affero General Public License v3.0 (AGPLv3) — see [`LICENSE`](LICENSE).
Free to use, modify, and redistribute. If you offer Grudge (or a modified
version) over a network, you must make the source — including your
modifications — available to users under AGPLv3.
