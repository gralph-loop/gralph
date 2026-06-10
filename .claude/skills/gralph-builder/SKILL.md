---
name: gralph-builder
description: >-
  Use this skill to design and build gralph workflows — the YAML profile plus
  external Lua scripts that drive a "ralph loop" (gralph run). Triggers: any
  request to create, edit, or review a gralph profile.yaml, gralph Lua
  validation/routing scripts, a gralph command graph, or a ralph-loop agent
  workflow; mentions of `gralph next`, `gralph run`, gralph.fail/route/store,
  or ".gralph-state". The skill's central job is to turn loosely specified
  agent work into a graph of deterministically-verified steps, so the loop only
  advances on machine-checkable evidence rather than the agent's self-report.
  Do NOT use for unrelated Go/Lua coding or for non-gralph agent frameworks.
---

# gralph-builder

Build gralph workflows whose every step is gated by **deterministic
verification**. This skill is opinionated: a gralph graph is only as trustworthy
as its Lua gates, so the work of building one is mostly the work of designing
gates the agent cannot bluff past.

## What gralph is (the 30-second model)

gralph runs a *ralph loop*: an orchestrator (`gralph run profile.yaml`) launches
a fresh, non-interactive agent session over and over. Inside each session the
agent:

1. runs `gralph next` to be told the **one** command it must eventually run,
2. does whatever non-deterministic work that command requires,
3. runs that command with arguments (e.g. `gralph verify --report r.json`).

That command executes a user-written **Lua script** that deterministically
checks the work. On success the cursor advances to the next graph node and the
session is told to end; on failure the agent retries *in the same session*,
until an n-th failure forces a fresh session. When the cursor reaches `DONE`
the orchestrator stops looping. The agent never sees the loop's stop signal.

The split that matters: **the agent is non-deterministic; the Lua gate is
deterministic.** The whole value of the framework is that progress is fenced by
code, not by the model claiming it finished. Your job as builder is to make
those fences real.

Read `reference/execution-model.md` for the precise contract, session scoping,
state files, and threshold math before writing a non-trivial profile.

## When you're invoked

Figure out where the user is and jump in:

- **"Build me a gralph workflow for X"** → run the authoring workflow below.
- **"Review / fix my profile"** → lint it (`scripts/lint_profile.py`), then audit
  every gate against the doctrine below and report self-attestation gates.
- **"How do I verify X deterministically in gralph?"** → go straight to
  `patterns/deterministic-gates.md` and pick a recipe.

Always confirm the actual checkable artifacts of the task before writing Lua.
If you don't know what file/exit-code/string the work produces, you can't gate
it — ask or inspect first.

## The deterministic-verification doctrine

This is the core of the skill. Apply it to every command you write.

**A gate must check evidence, not assertions.** The agent will happily run
`gralph verify --ok yes`. That proves nothing. A real gate inspects an artifact
the work necessarily produced and that the agent cannot fabricate cheaply:

| ❌ Self-attestation (avoid) | ✅ Evidence (prefer) |
|---|---|
| `--ok yes` / `--done true` | `--report tests.json` then Lua parses it and checks `failed == 0` |
| "I implemented it" | Lua runs `os.execute("go build ./...")` and checks exit code 0 |
| `--count 5` (agent's claim) | Lua runs `io.popen("ls dir | wc -l")` and reads the real count |
| "tests pass" | Lua re-runs the suite itself; the agent can't fake an exit code it didn't earn |
| `--summary "looks good"` | Lua checks the summary is non-empty AND the referenced file exists AND parses |

**The strongest gate runs the check itself.** When the verification is itself a
command (build, test, lint, type-check, schema-validate), have Lua *execute* it
via `os.execute`/`io.popen` rather than trusting an argument. The agent then has
no surface to lie on. See `reference/lua-bridge.md` for the exact, verified
semantics (`os.execute` returns the exit code as a number; `0` means success).

**When you must take the agent's word, make the word expensive and checkable.**
If a step is irreducibly subjective (e.g. "the prose reads well"), don't fake a
deterministic gate. Instead force *structured, falsifiable evidence*: require
the agent to submit file paths, line numbers, or a JSON report in a fixed shape,
and have Lua verify the shape, that the paths exist, that quoted lines actually
appear in the cited file, etc. You can't check taste, but you can check that the
agent did the looking. This both raises the floor and produces an audit trail.

**`gralph.fail(reason)` is a repair instruction, not a log line.** The reason is
shown to the agent so it can fix the problem and retry in the same session.
Write reasons that say exactly what was wrong and what to do:
`gralph.fail("reason: report.json missing key 'coverage'; add it and resubmit")`,
not `gralph.fail("invalid")`.

**Route on machine signals.** When a node has ≥2 successors, the Lua must call
`gralph.route(name)`. Base that choice on the same evidence you validated
(exit code, parsed count, presence of a marker), never on an argument that just
says where to go.

**Commit only what verification earned.** `gralph.store.set` is committed *only
on success*. Don't rely on a value persisting from a run that later failed (a
counter you bump and then `gralph.fail` on will not be saved). See the attempts
gotcha in `reference/execution-model.md`.

## Authoring workflow

1. **Map the work to a graph.** List the steps. Each step that needs a
   correctness fence is a command (node). Draw the edges (`next:`). Decide which
   nodes branch (≥2 successors → needs routing Lua) and which terminate (0
   successors → success sets cursor `DONE`).

2. **For each node, name the deterministic evidence.** Before writing guidance,
   answer: *what artifact proves this step is done, and what mechanical check
   confirms it?* If the answer is "the agent says so," redesign per the doctrine
   above. This is the step people skip; don't.

3. **Write the guidance** (`guidance:`). It is rendered by `gralph next` with
   `text/template`; only `{{store "key"}}` and `{{.Cursor}}` are available — **no
   Lua runs at render time.** Tell the agent what to produce and end with the
   exact `RUN:` line, including the args. Keep it imperative and specific.

4. **Declare args** the agent must pass (`args:`), marking `required: true` where
   the gate needs them. Arg values arrive in Lua as **strings**
   (`gralph.args.name`); use `tonumber()` for numerics.

5. **Write the Lua gate** (`lua:`, path relative to the profile). Pick a recipe
   from `patterns/deterministic-gates.md`. Validate, then on success
   `gralph.store.set` anything later guidance/gates need, and `gralph.route` if
   branching. On any problem `gralph.fail("reason: ...")` with a fix instruction.

6. **Lint and dry-run.** Run `python3 scripts/lint_profile.py profile.yaml`
   (static checks: schema, dangling successors, missing Lua files, ≥2 successors
   without Lua, self-attestation smells). Then build gralph and run the loop
   against a fake agent with `--max-iterations` to confirm the graph traverses
   and routes as intended (see "Testing" below).

## Validation rules gralph enforces (don't trip them)

The profile loader rejects, at load time: zero commands; a command with no
name; the reserved name `DONE`; duplicate command names; a `next:` entry naming
an unknown command; and **a command with >1 successor but no `lua:`** (nothing
could route it). A Lua `error()` (or bridge misuse) is reported as a SCRIPT
ERROR and still counts toward the failure threshold. Calling a command other
than the current cursor is rejected without consuming the failure budget, as are
argument-shape mistakes (unknown/missing args).

Full field-by-field schema: `reference/profile-yaml.md`.

## Testing a workflow

gralph is a Go program; build it once (`go build -o gralph .` in the gralph
repo, Go 1.22+), then from the profile's directory:

```
gralph run profile.yaml --max-iterations 12
```

For development, point `agent.command` at a **fake agent** that mimics the real
loop — call `gralph next`, run the `RUN:` line, obey "End the session", else
remediate and retry — so you can exercise the whole graph without a model. The
upstream repo's `example/test/agent.sh` is a working template. Each example in
`examples/` ships one. Watch the orchestrator's stderr (`[gralph] iteration N |
cursor X`) to confirm the path and routing.

## Reference map

- `reference/execution-model.md` — orchestrator/session contract, two state
  files, commit-on-success, threshold math, cursor-advance table, gotchas.
- `reference/profile-yaml.md` — complete YAML schema, defaults, validation.
- `reference/lua-bridge.md` — the `gralph.*` API, value conversion, and the
  **empirically verified** `os.execute`/`io.popen`/`io.open` idioms.
- `patterns/deterministic-gates.md` — copy-paste gate recipes (run-the-checker,
  parse-the-report, captured-output assertion, structured evidence, routing).
- `examples/tdd-loop/` — a coding loop whose gates *run the build and tests*.
- `examples/release-notes/` — a non-coding loop (research → draft → validate)
  showing structured-evidence gates for "soft" work.
- `scripts/lint_profile.py` — deterministic profile linter.
