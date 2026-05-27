# Contributing

Thanks for your interest in Spidey. This document covers the practical bits —
how to develop, test, and submit changes. For design rationale, see
[`ARCHITECTURE.md`](ARCHITECTURE.md).

## Development setup

Requires Go 1.25+, Node 20+, Docker, and [go-task](https://taskfile.dev).

```bash
git clone https://github.com/emontenegr/spidey.git
cd spidey
task substrate:up   # start TEI + vLLM + SearXNG containers
task build          # build the web bundle + the Go binary
task run            # serves on http://spidey.localhost:8420
```

## Before opening a PR

Run the full verification suite:

```bash
task verify
```

That runs `go vet`, the dependency-graph invariants, the Go test suite, and
the web (vitest) test suite. The CI workflow in `.github/workflows/ci.yml`
runs the same target — green locally means green in CI.

## Commit messages

Convention: short imperative subject line, optional body. Recent history is
the best reference:

```
git log --oneline -20
```

Keep changes focused. One concern per PR; one logical change per commit if
possible.

## Where things live

| Want to... | Look here |
|---|---|
| Change the RRC algorithm | `rrc/` |
| Add a model provider | `core/adapter/<name>/` + register in `init()` |
| Add a tool the agent can call | `service/agent/tools/` |
| Change a GraphQL schema field | `service/graph/schema.graphqls` → run `gqlgen generate` |
| Change a proto message | `proto/spidey/v1/*.proto` → `task proto` |
| Document a design decision | `ARCHITECTURE.md` |

## Reporting bugs / requesting features

Use the issue templates under `.github/ISSUE_TEMPLATE/`. Include a
reproduction or a clear motivation.

## Security

Don't file security issues as public GitHub issues. See
[`SECURITY.md`](SECURITY.md).
