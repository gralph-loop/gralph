# Examples

Two complete, runnable gralph workflows, each shipped with a scripted fake agent
so you can watch the whole graph traverse without a model. Both are verified to
run end-to-end and lint clean (`scripts/lint_profile.py`).

| Example | Domain | Gate patterns showcased |
|---|---|---|
| `tdd-loop/` | coding | run-the-checker (`os.execute` on the test/build command), routing on the real exit code, artifact-exists, semver validation |
| `release-notes/` | writing / docs | structured-evidence cross-check (cited PRs must exist in the source), content checks, in-session retry on a fixable failure |

For a fork/join (subcommand quota) workflow with a fake agent that spawns real
parallel workers, see the upstream repo's `example/subcommands/` — it
demonstrates per-item gates, a finalize gate using `gralph.progress.*`, and
resuming partial progress across sessions.

## Running either one

Put a `gralph` binary on your `PATH` (the fake agent calls `gralph next`). If
you don't have one, download it from the latest release
(`gralph-<os>-<arch>`, `.exe` suffix on Windows) or build from source
(Go 1.22+):

```sh
# download (adjust os/arch; see SKILL.md "Getting gralph"):
curl -fL -o gralph https://github.com/gralph-loop/gralph/releases/latest/download/gralph-linux-amd64
chmod +x gralph
# or build, in the gralph repo:
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
