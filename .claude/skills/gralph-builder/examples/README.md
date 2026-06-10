# Examples

Two complete, runnable gralph workflows, each shipped with a scripted fake agent
so you can watch the whole graph traverse without a model. Both are verified to
run end-to-end and lint clean (`scripts/lint_profile.py`).

| Example | Domain | Gate patterns showcased |
|---|---|---|
| `tdd-loop/` | coding | run-the-checker (`os.execute` on the test/build command), routing on the real exit code, artifact-exists, semver validation |
| `release-notes/` | writing / docs | structured-evidence cross-check (cited PRs must exist in the source), content checks, in-session retry on a fixable failure |

## Running either one

gralph is a Go program (Go 1.22+). Build it once and put the binary on your
`PATH` as `gralph` (the fake agent calls `gralph next`):

```sh
# in the gralph repo:
go build -o gralph .
# then, e.g.:
export PATH="$PWD:$PATH"

cd examples/tdd-loop
gralph run profile.yaml --max-iterations 12
```

Reset between runs with `rm -rf .gralph-state` (and the example's generated
files, e.g. `src/`, `changes.txt`, `NOTES.md`).

## Reading them

Start with the example's `README.md` for the graph and expected trace, then read
`profile.yaml` top-to-bottom and each `scripts/*.lua` gate. The gates are the
interesting part — every one inspects an artifact or runs a check rather than
trusting an argument. `agent.sh` only exists to stand in for a real agent during
testing; you don't ship it with a production profile (you point `agent.command`
at your real agent, e.g. `["claude", "-p", "{{prompt}}", "--dangerously-skip-permissions"]`).
