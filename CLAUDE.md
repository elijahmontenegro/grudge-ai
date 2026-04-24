# Spidey — Design Methodology

Principles this codebase commits to. Written for whoever (human or agent) picks it up next.

## UI

### Tool outputs render in their tool's slot

A tool call has exactly one home: its own collapsible in the conversation. Every tool output — the Bash stdout, the FileRead content, the pending question from AskUserQuestion, the plan from ExitPlanMode — belongs inside that slot. Nothing floats.

**Why.** A floating card elsewhere adds associative cost. The user has to: see the card, figure out which tool call it matches, scroll to that tool call, scroll back to answer. That's N steps where zero is possible. Interactive outputs aren't a different category from static ones; they're the same output with a richer renderer. Expanding/collapsing the tool controls its whole presence, the interactive bits included.

**What this looks like in code.** `ToolCall.tsx` accepts a `renderExtras` / `extras` slot. `TurnResponses` in `Turn.tsx` decides the extras per tool (AskUserQuestion → QuestionPrompt, ExitPlanMode → PlanApprovalCard). Neither prompt component is rendered anywhere else in the tree.

**The test.** Collapse the tool. Can the user still see / interact with its output somewhere? If yes, you're wrong — it leaked.

### Interactivity is a rendering variation, not a location

ToolApprovalPrompt (gates execution) is the one genuine exception: it's a pre-execution permission dialog, not an output. It can float because there is no tool-call slot yet. Once the call is live, its output belongs in the slot.

### Don't duplicate the chrome

Thread name shows in one place (topbar). Agent running status shows in one place (composer status strip). If a piece of chrome appears twice it's duplication — kill the weaker instance. A thread header with the same name the topbar already shows is noise.

### Empty states are load-bearing or nothing

An empty-state block is justified when it carries information the user couldn't derive otherwise (e.g. branched-thread inheritance explainer). A non-branched fresh thread has a composer right below saying "continue the thread" — a big "A fresh thread. No corpus yet." block on top of an incoming streaming response is redundant chrome.

## State

### Write on commit, not on intent

Clicking "New" shouldn't create a DB thread. The thread materialises on first successful send. Dead threads — created by users who clicked New and walked away — accumulate otherwise. Same principle anywhere else a click could commit to backend state before the user has actually committed.

**Implementation.** `/thread/new` is a draft URL. ThreadPage detects it, renders a composer with no corpus, and creates the thread on first `onSend` / `onStartAutonomous` before navigating to the real id.

### Backend rejections mustn't strand UI

If the user answers a stale question and the backend rejects the mutation (already answered, timeout expired, runner restarted), the dismiss still happens client-side. The user's intent takes precedence over the backend's knowledge. Mutation errors get logged; they don't propagate into stuck UI state.

### Interactive controls reflect real backend state

If a toggle exists, its backing query must fetch the field the toggle edits. A checkbox that sends UPDATE but reads from a query that doesn't return the updated field will appear to not persist — because the "next render" sync reads stale data and snaps the toggle back. When adding UI for a field, check what queries populate its source of truth first.

## Security

### Sandbox is a security boundary, not a UX affordance

When `sandboxed=true`, every file tool **and** shell goes through the container. Path tools (FileRead / FileWrite / FileEdit / NotebookEdit) must route through `sandbox.ResolveWorkspacePath` before any `os.ReadFile` / `os.WriteFile`. Bash goes through `sandbox.Sandbox`. Nothing escapes.

The sandbox exists to contain prompt injection — a model tricked into reading `C:\Users\...\.ssh\id_rsa` or writing to `/etc/passwd`. A split where Bash is sandboxed but FileWrite runs on the host defeats that. No exceptions.

**Workspace layout mirrors plans.** `{DataDir}/sandboxes/sbx-{threadID}/` sits next to `{DataDir}/plans/plan-{threadID}/`. Thread IDs are validated against `^thread-\d+$` before being interpolated into paths.

### Fail fast, not silent

No retries, no fallbacks. If Docker isn't running, emit the error at boot and return it from `Sandbox.Exec` — don't try to pull, don't try direct execution. If the sandbox image is missing, tell the user the exact command to build it. A silent degradation ("sandbox unavailable, running on host") is worse than a failure because the user thinks they're protected when they aren't.

### Default to the safe mode

New threads are `sandboxed=true` at creation. Users can opt out per-thread in the config popover. The opposite default — ship unsafe, let users turn on safety — is never the right trade.

## Storage

### SQLite needs WAL + busy_timeout actually applied

`modernc/sqlite` ignores `?_journal_mode=WAL&_busy_timeout=5000` DSN args silently. Pragmas must go via `?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)` or explicit `Exec("PRAGMA ...")` post-open. Verify `PRAGMA journal_mode` returns `wal` at startup and fail to boot if it doesn't — a silent revert to rollback mode turns every concurrent write into instant `SQLITE_BUSY`.

### Message IDs don't use sequential counters

`msg-{tid}-{counter}` looks fine until a round fails mid-commit, leaving `len(corpus) < max(id suffix)`. The next round's `counter = len + 1` collides. Use `msg-{tid}-{nanos}-{counter}` instead. No state to resync, no collisions after a failed round.

## Agent loop

### Dedupe at the storage boundary

ADK can re-emit the same `FunctionCall` or `FunctionResponse` part across multiple events in a single Run. Track `seenCallIDs` / `seenResultIDs` in the runner's event loop and skip duplicates on insert. Otherwise the corpus the RRC engine sees next round contains phantom retries the model never emitted.

### Tool feedback must reach the model

If a tool validation rejects the call, the error result must be stored so the next round's context includes it. Without that, the model flies blind and re-emits the same broken call. If storage is unreliable (see SQLite section above), the model *cannot* self-correct and you'll get multi-round retry loops with no visible progression.

## Writing

### No jargon in user-facing copy

"RRC will have nothing to select from on turn one" is jargon that leaked. "corpus" in a UI label is jargon. End-user copy uses plain words; the rationale behind the design stays in journal / doc / comments.

### Comments explain *why*

Not what — the code says what. Comments earn their line by explaining a non-obvious constraint, a past incident, a load-bearing invariant. A comment that restates the next line gets deleted.
