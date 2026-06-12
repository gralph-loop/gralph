# Profile YAML reference

Complete schema for a gralph profile, with defaults and the loader's validation
rules. Paths in the profile are resolved **relative to the profile file's
directory**.

## Top level

```yaml
agent:
  command: ["claude", "-p", "{{prompt}}", "--dangerously-skip-permissions"]
  timeout: 30m                  # optional; kill the session past this (Go duration)
prompt: |                       # optional; a sensible default is used if omitted
  You are running inside a gralph (ralph loop) session.
  1. Run "gralph next" to receive the gralph command you must eventually run.
  2. Do whatever is needed to be able to run it, then run it with its arguments.
  3. Whenever a command's response says to end the session, end it immediately.
state_dir: my-state             # optional; default ".gralph/<instance>" (relative to profile)
fail_threshold: 5               # optional; default 5; every n-th failure recycles the session
lua_timeout: 30s                # optional; default lua gate time limit (per-command override)
commands:                       # required; ≥1
  - ...
```

| Field | Required | Default | Notes |
|---|---|---|---|
| `agent.command` | for `gralph run` | — | argv list; every element may contain `{{prompt}}`, replaced with the ralph prompt. Not needed for in-session subcommands, but required to run the loop. |
| `agent.timeout` | no | none | Go duration string. A session exceeding it is killed (SIGTERM, then hard kill) and retried like any abnormal agent exit. |
| `prompt` | no | built-in default | The ralph prompt handed to the agent each session. |
| `state_dir` | no | `.gralph/<instance>` | Where `state.json` + `store.json` live. Relative paths resolve against the profile dir. The *instance name* is not a YAML field: it comes from `--name` (or `$GRALPH_INSTANCE_NAME` inside sessions), defaulting to the profile filename stem, so one profile definition can drive several isolated flows. The loader refuses to run when state from the legacy default (`.gralph-state`) would be silently abandoned; it prints the migration `mv`. |
| `fail_threshold` | no | `5` | Profile-wide failure threshold (per-command override available). Must be > 0. |
| `lua_timeout` | no | none | Go duration string; aborts a gate that runs longer (SCRIPT ERROR, counts toward the threshold). Per-command override available. |
| `commands` | yes | — | The graph nodes, in order. `commands[0]` is the entry node. |

## A command (one graph node)

```yaml
- name: verify                  # required; unique; must not be "DONE"
  guidance: |                   # rendered by `gralph next` (text/template)
    Verify the build of "{{store "goal"}}" (pass {{store "attempts"}}).
    RUN: gralph do verify --report report.json
  args:                         # arguments the agent must pass as --name value
    - name: report
      required: true
      desc: "path to the JSON test report"   # documentation only
  lua: scripts/verify.lua       # path relative to profile dir; optional
  next: [fix, finish]           # successor candidates
  fail_threshold: 3             # optional per-command override
  lua_timeout: 10s              # optional per-command gate time limit
  agent:                        # optional per-node agent override (e.g. cheaper model)
    command: ["claude", "-p", "{{prompt}}", "--model", "haiku"]
  prompt: |                     # optional per-node ralph prompt override
    ...
```

| Field | Required | Notes |
|---|---|---|
| `name` | yes | Unique across the profile. `DONE` and `do` are reserved and rejected; built-in CLI words are allowed (custom commands run as `gralph do <name>`). |
| `guidance` | recommended | Text returned by `gralph next` while the cursor is on this node. See templating below. |
| `args` | no | Each: `name` (required), `required` (bool, default false), `desc` (rendered in the auto-generated usage block). The agent passes them as `--name value` or `--name=value`. The arg names `profile` and `name` are reserved (the CLI consumes those flags itself). |
| `lua` | see rules | Path (relative to profile) to the validation/routing script. Optional — but **required if `next` has ≥2 entries**. Without Lua a command always succeeds. |
| `next` | no | Successor command names. 0 → terminal (success → `DONE`); 1 → unconditional; ≥2 → Lua must `gralph.route`. Every name must be an existing command. |
| `fail_threshold` | no | Overrides the profile threshold for this node only. |
| `lua_timeout` | no | Overrides the profile-level `lua_timeout` for this node's gate. |
| `agent` / `prompt` | no | Per-node overrides: while the cursor is on this node, sessions launch with this agent command / ralph prompt instead of the profile-level ones. A declared `agent` must have a non-empty `command`. |
| `subcommands` | no | Turns the node into a fork/join with quotas. See below. |

## Subcommands (fork/join quotas)

A command with `subcommands:` does not succeed once; instead, while the cursor
sits on it, each subcommand must succeed once per **distinct work-item key**,
`count` times in total. Only after every quota is met does the parent command
itself become runnable — it then acts as the finalize gate (aggregate
verification + routing) and its success advances the cursor. Built for agents
that can spawn parallel sub-agents: each worker runs one
`gralph do <subcommand> --<key> <item>` and the state-dir flock keeps concurrent
commits safe.

```yaml
- name: build-all
  guidance: |
    Remaining work: {{subprogress}}
    Spawn one sub-agent per remaining item.
    When all quotas are met RUN: gralph do build-all
  subcommands:
    - name: make-part            # shares the CLI namespace: globally unique
      count: 3                   # quota: 3 distinct keys (default 1)
      key: part                  # names the arg identifying the work item;
                                 # required when count > 1; forced required
      args:
        - name: part
      lua: scripts/part.lua      # per-item gate; gralph.route is forbidden
      fail_threshold: 3          # optional; budget counts per (sub, key)
  lua: scripts/finalize.lua      # finalize gate; sees gralph.progress.*
  next: [wrap]
```

Semantics worth designing around:

- A duplicate key, or running the parent before quotas are met, is rejected
  **without consuming the failure budget** (like a wrong-command call).
- Subcommand successes persist in `progress.json` across sessions; they reset
  only when the parent succeeds, so a graph cycle that revisits the node
  restarts the quotas.
- Subcommand success responses still say "End the session now" — a parallel
  sub-agent worker just ends itself; an agent without sub-agent support can
  do one item per session serially and resume.
- Store convention for parallel gates: namespace writes by key
  (`gralph.store.set("ev:" .. gralph.args.part, ...)`); commits merge per key.

## Guidance templating

`guidance` is rendered with Go's `text/template`. **No Lua runs at render time.**
Only two things are available:

- `{{store "key"}}` → the value from `store.json` for that key (empty string if
  unset). Use it to feed forward evidence written by an earlier gate (the goal,
  a path, a count, an attempt number).
- `{{.Cursor}}` → the current command's name.
- `{{usage}}` → the usage block generated from the node's `args` spec (exact
  invocation line plus an argument table built from `required`/`desc`).

Nodes with `subcommands:` additionally get `{{subprogress}}` (multi-line quota
view), `{{subdone "sub"}}` (completed keys) and `{{subcount "sub"}}`; `gralph
next` also auto-appends a progress block so a fresh session can always resume.

Do **not** hand-write the invocation line: `gralph next` derives a usage block
from the `args` spec and auto-appends it when the guidance never calls
`{{usage}}` (call it to control placement). Hand-written `RUN:` lines drift
from the spec; the generated block cannot. `gralph next` also auto-appends any
recorded failure reasons from earlier sessions, so the agent sees what already
went wrong.

## Validation performed at load (`gralph run` / first subcommand)

The profile is rejected if any of these hold:

- `commands` is empty.
- A command has an empty `name`.
- A command is named `DONE` (reserved sentinel).
- Two commands share a `name`.
- A `next:` entry names a command that doesn't exist.
- A command has more than one `next:` candidate but no `lua:` to route them.
- A subcommand has an empty or reserved name, or its name collides with any
  command or other subcommand (they share the CLI namespace).
- A subcommand has `count` > 1 but no `key`, or its `key` is not a declared arg.
- A command or subcommand is named `do` (the `gralph do <name>` namespacing
  word). Built-in CLI words (`run`, `next`, ...) are NOT rejected: the `do`
  namespace keeps them from ever colliding with custom commands.
- A node declares an `agent:` override with an empty `command`.
- An unparsable or non-positive `agent.timeout` / `lua_timeout`.

These are deterministic, static checks. `gralph validate profile.yaml` runs
them all without starting the loop, plus lua file existence, lua compile
checks, and graph reachability warnings. `scripts/lint_profile.py` reproduces
the schema checks with extra style/anti-pattern lints when the binary isn't
available.
