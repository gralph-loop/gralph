# GALP V1 — Gralph Agent Launcher Protocol

GALP is the language-neutral, process-to-process contract between gralph (the
**host**) and a **launcher** (a plugin: a separate program that knows how to
spawn and drive one agent session). gralph never execs an agent directly; it
execs a launcher and reads back a structured result. Everything specific to a
particular agent — interactive TTY automation, quota-reset waiting, permission
auto-answering — lives in the launcher, behind the process boundary, so
supporting a new agent never requires a new gralph release.

This document is the authoritative reference for protocol version **1**.

## At a glance

```
gralph (host)                                   launcher (plugin)
  │  write request.json + prompt.txt
  │  exec: <launcher argv...> -- <agent argv...>
  │  env: GALP_* + GRALPH_*  ───────────────────▶  spawn/drive the agent
  │                                                 (substitute {{prompt}} or
  │                                                  send-keys; detect quota …)
  │  ◀───────────────────────  write result.json, exit 0
  │  read result.json → act on outcome
```

The host and launcher communicate through **files + environment + exit code**.
Control information travels *only* through the result file — never stdout.

## Invocation (host → launcher)

```
exec: <launcher argv...> -- <agent command template argv...>
```

- Everything after `--` is the agent command template. A token may contain the
  placeholder `{{prompt}}`. The launcher decides the substitution policy
  (a subprocess launcher replaces it with the prompt text; a tmux launcher
  drops it and injects the prompt with `send-keys`).
- The launcher's working directory is the **profile directory**.
- The host inherits its own stdin/stdout/stderr to the launcher, so agent
  output streams through live.

## Environment (host → launcher)

| Variable | Meaning |
|---|---|
| `GALP_VERSION` | Protocol version integer. V1 = `1`. |
| `GALP_REQUEST_FILE` | Path to the request JSON — the authoritative source of all inputs. |
| `GALP_RESULT_FILE` | Path the launcher **must** write its result JSON to. |
| `GALP_PROMPT_FILE` | Path to the ralph prompt text (UTF-8). |
| `GALP_TIMEOUT_MS` | Advisory active-work limit in ms. `0` = unlimited. Does **not** apply to quota waiting. |
| `GALP_SESSION_ID` | The current session id. |
| `GRALPH_PROFILE` | Absolute profile path. **Pass through to the agent** so in-session `gralph next/do` find the same instance. |
| `GRALPH_INSTANCE_NAME` | Instance name. **Pass through to the agent** for the same reason. |

Two categories, do not confuse them:

- `GALP_*` — the protocol channel (host ↔ launcher). The launcher *consumes* these.
- `GRALPH_*` — gralph session passthrough. The launcher *must forward* these to
  the agent process unchanged; it does not consume them.

The `GALP_*` scalars are mirrored into env for convenience, but the
**authoritative input is the request JSON**. Rich/extension fields are added
only to the JSON; the scalar env set is fixed for V1.

## Request JSON (`GALP_REQUEST_FILE`)

```json
{
  "protocol": 1,
  "session_id": "1718521200-ab12cd34",
  "instance": "myflow",
  "profile": "/abs/path/profile.yaml",
  "dir": "/abs/path",
  "prompt_file": "/tmp/galp-XXXX/prompt.txt",
  "result_file": "/tmp/galp-XXXX/result.json",
  "agent_command": ["claude", "-p", "{{prompt}}", "--dangerously-skip-permissions"],
  "timeout_ms": 1800000,
  "env_passthrough": { "GRALPH_PROFILE": "...", "GRALPH_INSTANCE_NAME": "..." }
}
```

Future fields are **additive**; existing field meanings never change within V1.

## Result JSON (`GALP_RESULT_FILE`, launcher → host)

```json
{
  "protocol": 1,
  "outcome": "completed | crashed | rate_limited | timed_out | unstartable",
  "retry_after": "2026-06-15T08:00:00Z",
  "message": "optional human-readable note"
}
```

| Field | Required | Notes |
|---|---|---|
| `protocol` | ✅ | Must equal the host's `GALP_VERSION`. A mismatch is a clear host error. |
| `outcome` | ✅ | One of the vocabulary below. |
| `retry_after` | only for `rate_limited` | RFC3339. The host waits until this instant. A past time means retry immediately. |
| `message` | ❌ | Logging / diagnostics. |

### Outcome vocabulary (fixed for V1)

| outcome | host action |
|---|---|
| `completed` | Session ended normally. The host **independently** rechecks cursor progress with `resolveNext`; "completed" does **not** mean the graph advanced. |
| `crashed` | Abnormal exit. If the cursor did not move: backoff + the give-up budget (`MaxConsecutiveAgentFailures` = 5). |
| `timed_out` | Same handling as `crashed`, distinguished in logs. |
| `rate_limited` | Wait until `retry_after` (honoring ctx cancellation). Does **not** spend the give-up budget and `agent.timeout` does not apply. Then retry the same cursor. |
| `unstartable` | The agent binary itself cannot be started. Retrying is pointless, so the host gives up immediately (preserves the pre-GALP fail-fast on a missing agent command). |

> `completed` is a process-level statement only. The host decides graph
> progress on its own. A launcher reports what happened to the process, not
> whether work got done.

## Exit code (transport health)

- `0` — the launcher ran correctly and wrote a valid result file.
- non-zero — the launcher **itself** failed (including a missing/corrupt result
  file). The host treats this as `crashed` and ignores the result file. This
  separates an *agent* failure (reported via the result file) from a *launcher*
  failure.

## stdout/stderr

Pass-through channel for the agent's live output; the host inherits its own
stdout/stderr to the launcher. **Never** put control signals on stdout — all
control information goes through the result file.

## Versioning

- `GALP_VERSION` negotiates the version; the launcher echoes `protocol` in its
  result.
- Mismatch → the host fails that session with a clear error (which version it
  expected vs. received).
- Future fields are additive only. Changing the meaning of an existing field
  means V2.

## The built-in default launcher (first-class, zero-config)

If a profile sets no `launcher`, gralph re-invokes **itself** as
`gralph __galp-subprocess` (a hidden subcommand). This is the built-in
**subprocess** launcher: it reads the prompt file, substitutes `{{prompt}}`,
spawns the agent as a subprocess (inheriting stdio, forwarding `GRALPH_*`),
enforces
`GALP_TIMEOUT_MS`, forwards termination signals (SIGTERM → hard-kill), and
reports `completed` / `crashed` / `timed_out` / `unstartable`. It never reports
`rate_limited` — quota detection is the job of an opt-in launcher.

This is the **one launcher baked into the binary**, on purpose: the common
non-interactive case (run an agent as a subprocess) works with **nothing but the
single `gralph` binary** — no external files, no plugin to install, no network.
Every other launcher, official example or third-party alike, lives outside the
binary and is opt-in.

## Writing your own launcher

A launcher is just an **executable** (any language) that reads
`GALP_REQUEST_FILE` / the `GALP_*` env, drives the agent, writes
`GALP_RESULT_FILE`, and exits 0. There is no registration step and no plugin
API: you integrate one purely **by reference** — point a profile's `launcher:`
at it. This is identical for the official example launchers and for anything a
third party ships; the official examples have no special status.

```yaml
agent:
  command: ["myagent", "--flag", "{{prompt}}"]
  launcher: ["acme-launcher", "--verbose"]   # argv; the host appends `-- <agent argv...>`
```

`launcher:` may be set at the profile level (`agent.launcher`) or per node
(`commands[].agent.launcher`, which overrides the profile). The first token is
resolved like this:

| First token of `launcher:` | Resolved as |
|---|---|
| absolute path (`/opt/acme/launcher`) | used as-is |
| relative path **with a separator** (`./launchers/claude-tmux`) | relative to the **profile directory** |
| bare name **without a separator** (`acme-launcher`) | looked up on `PATH` (like a git subcommand) |

So a third party can distribute a launcher however they like — install it on
`PATH`, vendor it into the repo and reference it relatively, or pin an absolute
path. gralph never has to know about it ahead of time; that is the whole point
of the process boundary (supporting a new agent never needs a new gralph
release).

### Official example launchers

gralph ships a few **example** launchers in the release as a
`gralph-launchers-<version>.tar.gz` archive (and in this repo under
`launchers/`). They are ordinary plugin files — not embedded in the binary —
integrated by the exact same `launcher:` reference as any third-party launcher.
Copy one into your repo (or point at it on disk) and edit freely. (There is no
`subprocess` example: the built-in `__galp-subprocess` already covers the
non-interactive case, so duplicating it as a shell script would only risk
drifting from the Go implementation. For a minimal launcher to start from, read
the contract above and the `ratelimit` example.)

- `claude-tmux` — drives an interactive Claude Code session in a detached tmux
  session (dismiss the trust dialog, wait for the chat box, inject the prompt
  with `send-keys`, detect turn completion). The trust/ready/working markers are
  Claude-Code-specific; retune them to drive a different interactive TUI.
- `ratelimit` — scans agent output for a usage-limit signal and reports
  `rate_limited{retry_after}`.

```yaml
agent:
  # claude-tmux drives the INTERACTIVE TUI: no -p, and the prompt arrives via
  # send-keys (the {{prompt}} placeholder is dropped by the launcher).
  command: ["claude", "--dangerously-skip-permissions"]
  launcher: ["./launchers/claude-tmux"]   # relative path → resolved against the profile dir
```
