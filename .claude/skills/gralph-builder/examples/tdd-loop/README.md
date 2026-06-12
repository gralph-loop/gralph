# Example: TDD loop (run-the-checker gates)

A coding workflow where the correctness gate **runs the check itself**, so the
loop only advances on a real passing result — never on the agent's say-so.

## Graph

```
spec → implement → verify ─route→ { fix → verify | release }
```

- **spec** — name the feature (string validation). Stores `feature`, `passes=0`.
- **implement** — write the source; gate confirms every claimed file **exists**
  (artifact-exists, recipe 4).
- **verify** — gate **executes `./check.sh`** and routes on the real exit code:
  `0` → `release`, non-zero → `fix` (run-the-checker + routing, recipes 1 & 6).
  The agent passes *no* result flag; it cannot fake the outcome.
- **fix** — requires a non-trivial `--note` (audit trail); the real re-check
  happens when control returns to `verify`.
- **release** — strict semver tag validation; terminal node → `DONE`.

`check.sh` is a portable stand-in for `go test` / `pytest`: it exits 0 only once
`src/impl.txt` contains a `DONE` marker. In a real project, point the verify
gate's `os.execute` at your actual test/build command.

## Run it without a model

`agent.command` points at `agent.sh`, a scripted fake agent. Its first
implementation is intentionally incomplete, so `verify` routes to `fix`; the fix
step completes the implementation, and the next `verify` routes to `release`.

```sh
# build gralph (Go 1.22+) somewhere, then make it reachable as `gralph`:
#   go build -o gralph .   # in the gralph repo
# put the binary on PATH (the agent calls `gralph next`), then:
gralph run profile.yaml --max-iterations 12
```

Expected path (from the orchestrator's stderr):

```
spec → implement → verify (→fix) → fix → verify (→release) → release → DONE  (6 iterations)
```

After it finishes, `.gralph/tdd-loop/store.json` holds the accumulated evidence:
`feature`, `files`, `passes`, `last_fix`, `released`.

## What to copy into a real workflow

- The **verify** gate is the template: replace `sh ./check.sh` with your build +
  test command(s) (`go build ./... && go test ./...`, `npm test`, `pytest -q`,
  …). Keep routing tied to the exit code.
- Keep self-attestation out: notice no command takes an `--ok`/`--passed` flag.
- Lower `fail_threshold` on steps where a stuck agent should get a fresh context
  fast; this example uses 4.
