# Execution model

The precise mechanics of how a gralph workflow runs. Builders need this to
reason about retries, persistence, and routing.

## Two layers

gralph is one binary with two roles:

- **Orchestrator** — `gralph run profile.yaml [--max-iterations N]`. The ralph
  loop. You run this.
- **In-session subcommands** — `gralph next` and your YAML-defined custom
  commands. The *agent* runs these inside each session. The orchestrator injects
  `$GRALPH_PROFILE` so these find the profile automatically; `--profile` overrides.

## The loop (one iteration = one fresh session)

At the top of every iteration the orchestrator calls `resolveNext()` as a direct
function (never via the CLI):

1. If the cursor is `DONE` → break. The loop is finished. (With
   `--max-iterations N`, it also stops after N iterations.)
2. Otherwise: rotate the session id and **reset the per-command failure
   counters** (failures are session-scoped), then launch `agent.command` with
   `{{prompt}}` replaced by the ralph prompt.

The agent process is launched with its working directory set to the profile's
directory. The agent never observes the loop's termination — from its side every
session looks identical.

## What the agent does in a session

Driven by the prompt (default or your `prompt:`):

```
agent ── gralph next ─────▶ guidance for the current cursor node
                            (pure text/template render; {{store "k"}} only; NO Lua)
agent ── (non-deterministic work to satisfy that command)
agent ── gralph <cmd> --arg v ─▶ the node's Lua validates / routes / writes store
agent ── obeys the response: "End the session" → end now
```

## The command contract (every custom command obeys this)

When the agent runs `gralph <name> --args...`:

- **Wrong command** — if `<name>` isn't the current cursor, it's rejected
  ("not the current command"), exit 1. Does **not** consume the failure budget.
- **Argument-shape error** — unknown arg, missing value, or missing
  `required` arg → "usage error", exit 1. Does **not** consume the budget, and
  **no state is written**.
- **Validation failure** — Lua called `gralph.fail(reason)`, OR Lua `error()`d
  (reported as SCRIPT ERROR). The failure counter for this command increments.
  The store is **not** committed. The response tells the agent to fix it and
  retry *in this session* — **except** on every n-th failure (see threshold),
  where the response also says "End the session now", forcing a fresh
  session/context next iteration.
- **Success** — Lua neither failed nor errored (and, if the node has ≥2
  successors, called `gralph.route`). The cursor advances, the store is
  committed, and the response **always** tells the agent to end the session.

Key consequence: a command must succeed **exactly once** per visit, and only the
cursor command may run. The agent can't skip ahead or run a different node.

## Subcommands (fork/join quotas)

A node with `subcommands:` relaxes "exactly once" into a quota join. While the
cursor is on the parent:

- Each subcommand may run any number of times, but a success is recorded only
  for a **fresh work-item key** (the arg named by `key:`). Quota = `count`
  distinct keys per subcommand.
- A duplicate key, or running the parent before all quotas are met, is rejected
  like a wrong-command call: exit 1, **no failure budget consumed**.
- Once every quota is met, the parent itself runs as the finalize gate (its Lua
  sees `gralph.progress.*`); parent success clears the progress and advances
  the cursor as usual.
- Subcommand failures are budgeted per `(subcommand, key)`, so one stuck
  parallel worker doesn't recycle its siblings. Subcommand successes still end
  "the session" — for a parallel sub-agent that's just the worker ending.
- Recorded items live in **`progress.json`** (framework-owned, like
  `state.json`): they persist across session rotation (unlike failure counters)
  and reset only on parent success — so a cycle that revisits the node restarts
  its quotas, and a serial agent can resume one-item-per-session.
- Concurrency: parallel `gralph <subcommand>` processes serialize their
  read-modify-write commits via a flock on `<state_dir>/lock`. Gates run
  outside the lock, so slow verification stays parallel; the duplicate-key
  check is re-done at commit time, so racing workers on the same key produce
  exactly one recorded success.

## Cursor advancement (the `next:` list)

| `next:` candidates | behavior on success |
|---|---|
| 0 | last command — cursor becomes `DONE` (loop terminates) |
| 1 | unconditional move to that command |
| ≥2 | Lua **must** call `gralph.route("name")` with a listed candidate |

With ≥2 candidates: finishing the Lua without calling `route` is a SCRIPT ERROR;
routing to a name not in the list is a runtime error. Both count as failures.
The loader refuses to even start a profile that has ≥2 successors but no `lua:`.

## Failure threshold math

`fail_threshold` (profile-level default 5; per-command override via the
command's `fail_threshold`) controls session recycling. On the k-th failure of a
command within a session, if `k % threshold == 0` the response ends the session.
So with the default 5: failures 1–4 retry in-session, failure 5 forces a fresh
session, 6–9 retry, 10 forces again, etc. Lower it (e.g. 2–3) for steps where a
stuck agent should get a clean context quickly; raise it for steps where
in-session iteration is cheap and valuable.

Because the orchestrator resets counters when it rotates the session id, the
budget is **per session**, not global.

## State: two files, two owners

Under `state_dir` (default `.gralph-state`, resolved relative to the profile):

- **`state.json`** — framework-internal, off-limits to your Lua. Holds `cursor`,
  `session_id`, and per-command `failures`. You never read or write this from
  Lua; the framework owns it.
- **`store.json`** — the user KV store, **Lua-only**. `gralph.store.get/set`
  read/write it. The framework never interprets its contents. `gralph next`
  reads it to fill `{{store "key"}}` in guidance. Commits merge only the keys
  the succeeding run wrote, so parallel subcommand gates don't clobber each
  other.
- **`progress.json`** — framework-internal record of completed subcommand work
  items (key → timestamp/session). Separate from `state.json` because its
  lifecycle differs: survives session rotation, cleared only on parent success.
- **`lock`** — flock file serializing concurrent `gralph` processes'
  state commits (parallel sub-agent workers).

### Commit-on-success (and the attempts gotcha)

`gralph.store.set` changes are written to disk **only when the command
succeeds**. A failed validation leaves the store untouched — so a half-finished
gate never pollutes later steps. Practical implication:

> If a gate does `store.set("attempts", n+1)` and then `gralph.fail(...)` on the
> same run, the increment is discarded. A counter therefore tracks *successful*
> passes through the node, not total attempts. If you need to count failed tries
> too, that information lives in `state.json`'s `failures` (which you can't read
> from Lua) — design around it instead (e.g. let the threshold handle giving up).

## Reset / inspect

To restart a workflow from scratch, delete the state dir
(`rm -rf .gralph-state`). To inspect progress mid-run, read `state.json`
(`cursor` tells you where it is) and `store.json` (accumulated evidence).
