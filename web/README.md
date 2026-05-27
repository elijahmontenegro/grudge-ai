# Spidey web UI

React 19 + Apollo Client. Speaks GraphQL + WebSocket subscriptions to the
Spidey service. Renders the full RRC introspection surface — selection
trace, edges, prerequisite scores, prompt derivation, agent state, and the
tool execution timeline — alongside the thread / chat experience.

## Develop

```bash
npm install
npm run dev
```

The Vite dev server proxies GraphQL to a running Spidey backend (see
`vite.config.ts`). Start the backend first via `task run` from the repo
root.

## Build

The web bundle is built into `web/dist/` and then embedded into the Go
binary via `//go:embed`. The repo root's `task build` does both in one step
via the `embed-sync` Taskfile target. Don't ship `web/dist/` independently —
the binary serves the embedded copy.

## Folder convention

```
src/
├── App.tsx           top-level router + layout
├── domain/           business types (Thread, Message, Selection)
├── features/         feature folders — one directory per UI concern
├── primitives/       design-system atoms (Button, Input, Card)
├── state/            global Apollo + reactive vars
├── graphql/          .graphql operations + generated types
├── hooks/            shared hooks
├── lib/              cross-cutting utilities
├── data/             fixtures used in tests/stories
└── test/             vitest setup
```

Adding a new UI feature: create `src/features/<name>/`, colocate its
GraphQL operations under `src/graphql/`, depend on `primitives/` for
atoms. Avoid pulling business types from `features/` siblings; depend on
`domain/` instead.

## Test

```bash
npm test    # vitest run
```
