# Profile YAML reference

Complete schema for a gralph profile, with defaults and the loader's validation
rules. Paths in the profile are resolved **relative to the profile file's
directory**.

## Top level

```yaml
agent:
  command: ["claude", "-p", "{{prompt}}", "--dangerously-skip-permissions"]
prompt: |                       # optional; a sensible default is used if omitted
  You are running inside a gralph (ralph loop) session.
  1. Run "gralph next" to receive the gralph command you must eventually run.
  2. Do whatever is needed to be able to run it, then run it with its arguments.
  3. Whenever a command's response says to end the session, end it immediately.
state_dir: .gralph-state        # optional; default ".gralph-state" (relative to profile)
fail_threshold: 5               # optional; default 5; every n-th failure recycles the session
commands:                       # required; ≥1
  - ...
```

| Field | Required | Default | Notes |
|---|---|---|---|
| `agent.command` | for `gralph run` | — | argv list; every element may contain `{{prompt}}`, replaced with the ralph prompt. Not needed for in-session subcommands, but required to run the loop. |
| `prompt` | no | built-in default | The ralph prompt handed to the agent each session. |
| `state_dir` | no | `.gralph-state` | Where `state.json` + `store.json` live. Relative paths resolve against the profile dir. |
| `fail_threshold` | no | `5` | Profile-wide failure threshold (per-command override available). Must be > 0. |
| `commands` | yes | — | The graph nodes, in order. `commands[0]` is the entry node. |

## A command (one graph node)

```yaml
- name: verify                  # required; unique; must not be "DONE"
  guidance: |                   # rendered by `gralph next` (text/template)
    Verify the build of "{{store "goal"}}" (pass {{store "attempts"}}).
    RUN: gralph verify --report report.json
  args:                         # arguments the agent must pass as --name value
    - name: report
      required: true
      desc: "path to the JSON test report"   # documentation only
  lua: scripts/verify.lua       # path relative to profile dir; optional
  next: [fix, finish]           # successor candidates
  fail_threshold: 3             # optional per-command override
```

| Field | Required | Notes |
|---|---|---|
| `name` | yes | Unique across the profile. `DONE` is reserved and rejected. |
| `guidance` | recommended | Text returned by `gralph next` while the cursor is on this node. See templating below. |
| `args` | no | Each: `name` (required), `required` (bool, default false), `desc` (doc only). The agent passes them as `--name value` or `--name=value`. |
| `lua` | see rules | Path (relative to profile) to the validation/routing script. Optional — but **required if `next` has ≥2 entries**. Without Lua a command always succeeds. |
| `next` | no | Successor command names. 0 → terminal (success → `DONE`); 1 → unconditional; ≥2 → Lua must `gralph.route`. Every name must be an existing command. |
| `fail_threshold` | no | Overrides the profile threshold for this node only. |

## Guidance templating

`guidance` is rendered with Go's `text/template`. **No Lua runs at render time.**
Only two things are available:

- `{{store "key"}}` → the value from `store.json` for that key (empty string if
  unset). Use it to feed forward evidence written by an earlier gate (the goal,
  a path, a count, an attempt number).
- `{{.Cursor}}` → the current command's name.

Always end guidance with the exact `RUN:` line the agent should execute,
including arguments, so there's no ambiguity about what command closes the node.

## Validation performed at load (`gralph run` / first subcommand)

The profile is rejected if any of these hold:

- `commands` is empty.
- A command has an empty `name`.
- A command is named `DONE` (reserved sentinel).
- Two commands share a `name`.
- A `next:` entry names a command that doesn't exist.
- A command has more than one `next:` candidate but no `lua:` to route them.

These are deterministic, static checks. `scripts/lint_profile.py` reproduces
them (plus extra style/anti-pattern lints) so you can catch problems before
building or running gralph.
