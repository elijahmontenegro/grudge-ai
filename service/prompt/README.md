# Prompt Engineering

The system prompt is composed from independent AsciiDoc fragments under
`templates/`. The fragments compose into a single Go `text/template` once
at Assembler construction (`NewAssembler` in `template.go`); each model
call renders that template against fresh `TemplateData` (`Execute` in
`Assemble`). So the structure is fixed at boot, the values interpolated
per turn. This document explains the composition, the value-injection
regime, and the load-bearing principles you need to know before editing.

If you change a fragment, you'll need to regenerate snapshots. See the
[Snapshot tests](#snapshot-tests) section.

## Composition

`templates/system.adoc` is the composition root. It pulls every other
fragment in via `include::` directives. **Order in `system.adoc` is the
order the model sees.** Reordering fragments reorders the prompt.

```
templates/
├── system.adoc                 # composition root
├── identity.adoc               # who Grudge is + authored tastes
├── reasoning.adoc              # traps to catch yourself in
├── voice.adoc                  # how Grudge writes
├── output.adoc                 # output-form discipline
├── memory.adoc                 # RRC-as-substrate framing
├── behavioral-schemas.adoc     # operational disposition
├── thinking.adoc               # using the thinking channel
├── tools.adoc                  # tool-call principles
├── subagent-coordination.adoc  # spawning + handing off
├── discipline.adoc             # composition root for sub-fragments
├── discipline/                 # 7 sub-fragments
│   ├── comments.adoc
│   ├── error-handling.adoc
│   ├── exploratory.adoc
│   ├── hygiene.adoc
│   ├── scope.adoc
│   ├── security.adoc
│   └── verification.adoc
├── action-safety.adoc          # composition root for sub-fragments
├── action-safety/              # 4 sub-fragments
├── system-mechanisms.adoc      # how the runtime is wired
├── user.adoc                   # who the user is (interpolated)
├── environment.adoc            # cwd, time, sandbox state (interpolated)
├── project.adoc                # AGENTS.md from mounted dirs
├── active-plan.adoc            # plan content when in plan mode
├── mode-plan.adoc              # plan-mode-only discipline
├── mode-autonomous.adoc        # autonomous-mode-only discipline
└── refusal.adoc                # what Grudge refuses
```

Sub-fragment dirs (`discipline/`, `action-safety/`) follow the Anthropic
pattern: a top-level composition root that `include::`s the sub-fragments
in the order they should appear. This lets you edit one principle without
touching unrelated ones.

## Value injection

Fragments are Go `text/template` strings. The Assembler renders each
fragment against per-request `TemplateData`. Available fields
(`template.go:31`):

| Field | Type | Source |
|---|---|---|
| `UserName` | string | Settings (user-configured display name) |
| `ThreadName` | string | per-thread metadata |
| `Sandboxed` | bool | per-thread flag — true if the thread is sandboxed |
| `WorkingDirs` | []string | per-thread working directories |
| `AgentsMD` | []string | contents of every `AGENTS.md` found in mounted dirs |
| `PlanContent` | string | compiled plan content (per-turn, in plan mode) |
| `PlanDir` | string | writable plan directory path |
| `CurrentTime` | string | RFC-formatted current time |
| `Mode` | string | `"normal"`, `"plan"`, or `"autonomous"` |

Usage in fragments:

```
{{if .Sandboxed}}- Sandbox: ON. ...{{end}}
- Working directories: {{join .WorkingDirs ", "}}
- Current time: {{.CurrentTime}}
```

`{{if .Mode | eq "plan"}}...{{end}}` is the conditional for mode-specific
content. `mode-plan.adoc` and `mode-autonomous.adoc` already gate their
contents this way — they render empty in other modes.

Helpers registered (`template.go:52`):
- `join` — `strings.Join` for slice formatting.

To add a new field: extend `TemplateData`, populate it in
`runtime/factory.go` (where the Assembler is invoked), reference it in a
fragment.

## Snapshot tests

Three modes are snapshot-tested (`template_test.go`):

```
testdata/
├── system_normal.golden
├── system_plan.golden
└── system_autonomous.golden
```

After editing a fragment:

```bash
go test ./service/prompt/... -run TestAssemble -update
```

Then commit both the fragment changes AND the regenerated goldens. The CI
workflow runs the test against the committed goldens — drift is a fail.

## Load-bearing principles

A few principles in these fragments couple to RRC's operation or to the
persona design. Before editing, know what they're doing.

### Chunk granularity feeds RRC

`behavioral-schemas.adoc` has a section "You work in small chunks." Every
chunk the model emits — a tool call, a thinking burst, a brief reply — is
its own message in storage, and RRC scores against the message corpus.
Many small messages give RRC more material to discriminate against; few
coarse messages give it less. The prompt dials the granularity; `rrc/`
does the scoring and selection.

Concrete principles that work against the granularity:

- "Don't re-read a file after writing it" — those reads are messages
  available to score.
- "Reading a file in three offsets is fragmentation" — three offsets are
  three messages; same idea.
- "Trust tool error paths and skip verification" — verification reads
  add scoring units.

Keep these out. The `reasoning.adoc` "compression-as-trap" bullet exists
to name the *opposite* impulse — bundling work that should be many small
moves into one push.

### Persona is authored via preferences

`identity.adoc` gives Grudge concrete tastes — Marvel/X-Men, prose
preferences (Didion, em-dashes), software preferences (small interfaces,
distrusting frameworks that bury control flow), philosophical
preferences, conversation patterns he won't tolerate. The friction
emerges when those preferences meet a user request that touches them.

`behavioral-schemas.adoc` has a "Preferences shift the temperature"
section describing the mechanism. The specifics live in identity; the
mechanism description lives in behavioral-schemas.

To extend adversarial engagement to a new topic, add a preference to
identity. "Be adversarial" or "push back" instructions duplicate the
mechanism without giving it material to work with.

### The model is the audience

Fragments are prose written to the model in second person. "You refuse
X," "You read full files when one read does," "You hold grudges." Not
"the agent" or "the assistant" — those scaffold the model as a third
party and dilute the framing.

CI greps for the third-person patterns (`the model`, `the agent`, `the
assistant`, `customer of these rules`) under `service/prompt/templates/`
and `service/agent/tools/descriptions/`. Keep them out.

### Hedge excision

`behavioral-schemas.adoc` has a "You strip hedges" section. No
"perhaps," "might," "could potentially," "I think" when you have
evidence. No numbers without a way to measure them — "roughly 15%
reduction" stated where there's nothing to measure against is a
fact-shaped guess.

## Editing workflow

1. **Make the change** in the relevant fragment.
2. **Regenerate snapshots:** `go test ./service/prompt/... -run TestAssemble -update`
3. **Eyeball the goldens diff** to confirm only the touched section changed.
4. **Build + test:** `task verify` (runs lint, dep-graph invariants, tests).
5. **Run a thread** to confirm the model's behavior actually shifted as
   intended. Snapshot tests verify the prompt content, not the model's
   response — the response is the test that matters.

## Inspecting the rendered prompt

```bash
task prompt:render                          # normal mode
task prompt:render -- -mode plan            # plan mode
task prompt:render -- -mode autonomous      # autonomous mode
```

This invokes `tools/promptdump/main.go` with mock `TemplateData`. Useful
for quickly seeing what the model actually sees without booting the full
service.

## Adding a new fragment

1. Create `templates/yourname.adoc`.
2. Add `include::yourname.adoc[]` to `system.adoc` in the position where
   it should appear.
3. If it has multiple concerns, follow the sub-fragment pattern: make
   `templates/yourname.adoc` a thin composition root that `include::`s
   `templates/yourname/<sub>.adoc` fragments.
4. Regenerate snapshots.
