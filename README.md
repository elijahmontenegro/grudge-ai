# Spidey

Spidey is an LLM agent framework with a different approach to memory.
Most agents replay the full conversation history on every model call —
Spidey doesn't. Each turn, it picks only the prior turns the new turn
*depends on* and slips them back into the input as ordinary
conversation entries: no template, no "Here is some relevant context:"
header, no formatting. The model can't tell which turns were retrieved
from any other turn in the session.

<!-- Drop a screenshot at docs/assets/screenshot.png and uncomment: -->
<!-- ![Spidey UI](docs/assets/screenshot.png) -->

## Why this is different

Two patterns dominate agent memory today:

- **Re-send everything.** The default. Works fine until the history
  gets big — then the model starts forgetting instructions, contradicting
  earlier turns, and behaving like it didn't see what just happened.
- **Wrap retrieved snippets in a context block.** RAG-shaped: a
  retriever finds relevant passages, the application formats them into
  `Here is some relevant context: …` and pastes that into the prompt.
  The model treats the block as a hint or supplementary material — not
  as something it said.

Both leak the seam between retrieval and conversation. The model knows
when it's being handed retrieved text, and it adjusts accordingly.

Spidey's technique — **Retrieval-Restored Continuation (RRC)** — closes
that seam. Conversation history is stored losslessly. On each new turn,
a two-stage retriever picks the prior turns that turn depends on
(prerequisite detection, not topical similarity), and those turns slot
into the input in their original shape — same role tags, same content
blocks, same position semantics as the live session. There's no
framing for the model to "see retrieval" through, because the input
isn't framed as having any. It just looks like a shorter version of
the same conversation, with the load-bearing past included and the
irrelevant past elided.

RAG *augments*. RRC *restores*. Different verbs, different consequences.

See [the paper](docs/rrc-paper.md) for the threshold mechanism,
reflective selection (mid-generation re-retrieval driven by the model's
own reasoning), and comparison to neighboring frameworks (Letta, Zep,
FLARE, Jeong's reconstructed contexts).

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
