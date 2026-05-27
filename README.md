# Spidey

Selective memory assembly for LLM agents. Built on **Retrieval-Restored
Continuation (RRC)** — instead of prepending the full conversation history
on every model call, Spidey selects the specific prior turns the new turn
depends on and restores them as native conversation entries. The model
receives a bounded input that looks like ordinary conversation history;
it has no mechanism to tell which turns were retrieved.

<!-- Drop a screenshot at docs/assets/screenshot.png and uncomment: -->
<!-- ![Spidey UI](docs/assets/screenshot.png) -->

## RRC in a nutshell

LLM inference is stateless: every call is an independent forward pass.
The "conversation" is constructed entirely by whoever assembles the
input. The standard approach is brute force — prepend everything, every
time. That has a soft ceiling (attention quality degrades as positions
dilute; the advertised context limit is a product boundary, not an
architectural wall) and per-turn cost that grows with history.

RRC replaces brute force with selective assembly, defined by four
properties:

1. **Self-referential corpus.** Retrieval targets the model's own past
   turns, stored losslessly. No external knowledge base, no
   summarization, no write-time graph extraction. Lossy transformations
   irreversibly discard information based on what *looked* relevant at
   write time; relevance is decided at read time by the new turn, not
   fixed when the prior turn was stored.

2. **Prerequisite detection, not similarity.** Two-stage retrieval
   (bi-encoder candidates → cross-encoder filter) tuned with three-gate
   hyperselective thresholding. The new turn's *prerequisites* — the
   prior turns it depends on for coherent continuation — not topical
   matches. Zero-return is a valid outcome.

3. **Native format restoration.** Selected turns enter the input
   structurally identical to entries from the current session — no
   "Here is some relevant context:" wrapper, no augmentation template,
   no formatting transformation. The model processes them through the
   same attention computation it applies to any conversation input.

4. **Bounded assembly.** Selection size is determined by the three
   threshold gates (z-score, cross-encoder floor, batch standard
   deviation), not by a fixed quota or by conversation length.
   Zero-return is a valid outcome. What passes is whatever the gates
   admit.

This is **structurally distinct from RAG**. RAG *augments* — labeled
retrieved text inserted into a prompt template, generated against
external knowledge bases. RRC *restores* — native conversation turns
from the model's own history, indistinguishable on the wire from session
entries. One consequence is operational: per-turn compute is bounded by
retrieval top-K, not by conversation length. Another is behavioral: the
model continues as if it had remembered, because the input is
structurally what it would be if it had.

For the formal statement — including reflective selection (mid-
generation re-retrieval driven by the model's own reasoning output),
threshold calibration, and comparison to neighboring techniques (RAG,
FLARE, Zep, Letta, selective context reconstruction) — see
[the paper](docs/rrc-paper.md).

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
