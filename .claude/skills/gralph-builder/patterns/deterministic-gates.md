# Deterministic gate recipes

Copy-paste Lua gates, ordered roughly from strongest (gate runs the check
itself) to weakest-but-sometimes-necessary (structured evidence for subjective
work). Pick the strongest one the step admits.

For each recipe: what it proves, when to use it, and the Lua. All assume the
node declares the args it reads and lists the right successors.

---

## 1. Run-the-checker (strongest)

**Proves:** a command-defined property (builds, tests pass, lint clean,
types check, schema valid) — by *executing the check*. The agent has nothing
to lie about; it must actually make the check pass.

**Use when** verification is itself runnable. This is the default for any
coding workflow.

```lua
-- build_and_test gate
if os.execute("go build ./... >/dev/null 2>&1") ~= 0 then
  gralph.fail("reason: build failed; run `go build ./...` and fix errors")
  return
end
if os.execute("go test ./... >/dev/null 2>&1") ~= 0 then
  gralph.fail("reason: tests failed; run `go test ./...` and fix them")
  return
end
gralph.store.set("green", true)
```

`os.execute` returns the exit code as a number; `0` = success (see
`reference/lua-bridge.md`).

---

## 2. Parse-the-report

**Proves:** the work produced a machine-readable report with the required
shape/values. Stronger than a yes/no flag because the numbers must be real.

**Use when** the work emits JSON/structured output (test runners, coverage,
benchmarks, validators). Pair with a real parser via `jq` when available.

```lua
-- coverage gate: report must exist and show coverage >= 80
local report = gralph.args.report
if io.open(report, "r") == nil then
  gralph.fail("reason: '" .. report .. "' missing; write the JSON report first")
  return
end
-- jq -e exits non-zero if the filter is false/null -> a true deterministic check
if os.execute("jq -e '.coverage >= 80' " .. report .. " >/dev/null 2>&1") ~= 0 then
  gralph.fail("reason: coverage < 80 (or key missing) in " .. report)
  return
end
gralph.store.set("coverage_report", report)
```

No `jq`? Fall back to tight `string.find` patterns, but anchor them so they
can't be satisfied by an empty/garbage file.

---

## 3. Captured-output assertion

**Proves:** a real count/value taken from the system, not from the agent's
claim.

**Use when** "how many / which / does it contain" is the gate and a shell
one-liner can compute the ground truth.

```lua
-- expect exactly N migration files committed
local p = io.popen("git status --porcelain migrations/ | wc -l")
local changed = tonumber((p:read("*a"))); p:close()
if changed == 0 then
  gralph.fail("reason: no migration file staged under migrations/")
  return
end
```

---

## 4. Artifact-exists + well-formed

**Proves:** the deliverable file exists and minimally parses. The floor for any
"produce a file" step.

```lua
local path = gralph.args.path
local f = io.open(path, "r")
if f == nil then gralph.fail("reason: '" .. path .. "' was not created"); return end
local body = f:read("*a"); f:close()
if #body < 50 then
  gralph.fail("reason: '" .. path .. "' is suspiciously short (" .. #body .. " bytes)")
  return
end
gralph.store.set("artifact", path)
```

---

## 5. Structured-evidence (for irreducibly subjective work)

**Proves:** *not* that the prose/design is good (uncheckable), but that the
agent did the falsifiable work and produced an audit trail. Force it to cite
specifics, then verify the citations.

**Use when** the step's quality is a matter of judgment but you can still demand
checkable evidence: file paths that must exist, quoted lines that must actually
appear, a fixed-shape rationale.

```lua
-- review gate: agent must cite a real file:line and the quoted text must match.
local cite = gralph.args.evidence        -- e.g. "src/auth.go:42"
local quote = gralph.args.quote          -- the line it claims is there
local file, line = string.match(cite, "^(.+):(%d+)$")
if file == nil then
  gralph.fail("reason: --evidence must be path:line, got '" .. tostring(cite) .. "'")
  return
end
-- pull that exact line and compare
local p = io.popen("sed -n '" .. line .. "p' " .. file .. " 2>/dev/null")
local actual = p:read("*a"); p:close()
actual = (actual or ""):gsub("%s+$", "")
if not actual:find(quote, 1, true) then
  gralph.fail("reason: line " .. line .. " of " .. file ..
              " does not contain the quoted text; cite the real location")
  return
end
gralph.store.set("reviewed_at", cite)
```

The agent can still write a weak review, but it cannot invent a citation — and
the trail is recorded.

---

## 6. Routing on a machine signal

**Proves:** the branch was chosen by evidence, not by the agent's preference.
Use the same check that validates the node to decide the route.

```lua
-- verify node, next: [fix, finish]
-- Route by the real test outcome, not by an --ok flag.
if os.execute("go test ./... >/dev/null 2>&1") == 0 then
  gralph.route("finish")
else
  gralph.route("fix")          -- success path; the *defect* sends us to fix
end
```

Note this gate *succeeds* either way (it validly observed the state) and routes
accordingly. If instead you want "tests must pass to leave this node," use
recipe 1 and let `next: [verify]` from a `fix` node form the retry loop (see
`examples/tdd-loop`).

---

## Anti-patterns to refuse or rewrite

- **`--ok yes` / `--done true`** with Lua that only checks the literal string.
  This gates nothing; the agent always passes it. Rewrite to inspect an
  artifact or run the check.
- **Trusting an agent-supplied count/metric** (`--count`, `--coverage`) without
  recomputing it. Recompute with recipe 3 or 2.
- **`gralph.fail("failed")`** — uninformative. The reason is the agent's repair
  instruction; say what's wrong and what to do.
- **Bumping a counter then failing** — the `store.set` is discarded on failure
  (see the attempts gotcha in `reference/execution-model.md`). Don't rely on it.
- **A branching node with no `lua:`** — the profile won't even load.
