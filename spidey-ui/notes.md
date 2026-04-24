# Spidey Web UI — design notes

## The one idea

A thread is a **lossless corpus**, not a chat log. The "view" at any turn is the
RRC **selection** — the subset of the corpus the model actually saw. So the
thread UI is not an append-only stream. It's a **re-rendering view over an
immutable corpus**, mutating each turn based on selection.

Reading top-to-bottom reads *selections*, not chronology.

## What this kills

- "New chat" (threads are forever — branch instead)
- Memory settings, pinning, saved memories (corpus IS memory)
- Context-window UI (compact button, token counters)
- "Relevant context" labels (restoration is native)

## What this adds

- **Per-turn prerequisite strip** above each response: the restored messages
- **View mode per turn:** selections (default) / corpus / minimal
- **Corpus rail** (left gutter): density strip of the whole corpus, marking
  which messages participated this turn
- **Citation drawer:** slides in from right with score breakdown + QUD + DAG
- **Thread warmth** in the header (carry-forward count, structural prediction)

## System

- IBM Plex Sans + IBM Plex Mono
- Monochrome + single amber accent for RRC signals only
- Warm paper neutrals
- Editorial density: document-like thread surface, diagrammatic introspection

## Screens

1. **Thread view** — primary surface
2. **Home** — corpus list (not conversation list) + command palette + activity
3. **Introspection drawer** — citations → score breakdown → QUD graph → DAG
4. **First-run** — provider detection
5. **Autonomous mode** — running state
