# Lua bridge reference

What a gate script can do. The runtime is **gopher-lua** (pure-Go Lua 5.1),
embedded in gralph. The script runs once per command invocation, with the
agent's args and the user store exposed through the global `gralph` table.

> All idioms below were verified empirically against the gralph binary. Where
> behavior differs from reference Lua, it's called out.

## The `gralph` helper

| Call | Returns / effect |
|---|---|
| `gralph.args.<name>` | The arg's value as a **string** (declared args only). Use `tonumber()` for numbers. Absent → `nil`. |
| `gralph.store.get("key")` | The stored value (scalar, or nested table for JSON arrays/objects). `nil` if unset. |
| `gralph.store.set("key", v)` | Write to the user store. **Committed only if the command succeeds.** Accepts scalars and (nested) tables. |
| `gralph.route("name")` | Choose the successor when the node has ≥2 candidates. `name` must be in the node's `next:` list, else runtime error. No-op-meaningful for 0/1 successors. |
| `gralph.fail("reason: ...")` | Mark a validation failure. The script may keep running; first reason wins. If never called (and no `error()`), the run succeeds. The reason is shown to the agent as a repair instruction. |

### Success vs failure vs crash

- **Success**: script finishes, `gralph.fail` was never called, no `error()` —
  and if ≥2 successors, `gralph.route` was called with a valid candidate.
- **Validation failure**: `gralph.fail(reason)` was called. Reported as
  `FAILED ... reason`. Counts toward the threshold. Store not committed.
- **Script error**: Lua `error()` or a bridge misuse (e.g. `route` to a
  non-candidate, finishing with ≥2 successors but no `route`). Reported as
  `SCRIPT ERROR`. Also counts toward the threshold. Prefer `gralph.fail` for
  *expected* validation problems; let `error()` surface genuine bugs.

A no-op gate (`lua` set but the script does nothing) always succeeds — only use
that for a node with 0 or 1 successor where there is genuinely nothing to check.

## Value conversion (Lua ⇄ JSON store)

The store is JSON-backed. Conversion:

- Lua `nil` ⇄ JSON null; `boolean` ⇄ bool; `number` ⇄ number (stored as float);
  `string` ⇄ string.
- A Lua **table with consecutive integer keys 1..n** ⇄ JSON **array**.
- Any other Lua table ⇄ JSON **object** (keys stringified).
- Reading back: JSON arrays become integer-keyed tables, objects become
  string-keyed tables. So `store.set("paths", {"a.go","b.go"})` reads back as a
  table you index `[1]`, `[2]`.

Numbers round-trip as floats; if you stored `0` you get `0.0` back — compare with
`tonumber()`/numeric ops, not string equality.

## Running real checks from the gate (the strongest pattern)

gopher-lua ships the `os` and `io` standard libraries, so a gate can execute the
verification itself instead of trusting an argument.

### `os.execute` — run a command, branch on exit code

**Verified semantics:** in this runtime `os.execute(cmd)` returns the exit code
as a **single number** (Lua 5.1 style). `0` means success; non-zero is the
command's exit status. It does **not** return the `(ok, "exit", code)` triple of
Lua 5.2+.

```lua
-- Gate: the project must build. The agent cannot fake a passing build.
if os.execute("go build ./... >/dev/null 2>&1") ~= 0 then
  gralph.fail("reason: `go build ./...` failed; fix compile errors and resubmit")
  return
end
```

Quote/escape any value interpolated from `gralph.args` — it's agent-controlled
input. Prefer passing a path arg and validating its shape, then running a fixed
command, over interpolating arbitrary strings into a shell line.

### `io.popen` — capture output and assert on it

**Verified:** `io.popen` is available and captures stdout.

```lua
-- Gate: exactly the expected number of generated files exist.
local p = io.popen("ls build/out/*.html 2>/dev/null | wc -l")
local n = tonumber((p:read("*a"))); p:close()
if n ~= 3 then
  gralph.fail("reason: expected 3 generated pages, found " .. tostring(n))
  return
end
```

### `io.open` — read and inspect an artifact

```lua
-- Gate: the report exists and is well-formed for our needs.
local f = io.open(gralph.args.report, "r")
if f == nil then
  gralph.fail("reason: report '" .. gralph.args.report .. "' does not exist")
  return
end
local body = f:read("*a"); f:close()
if not string.find(body, '"status"%s*:%s*"pass"') then
  gralph.fail("reason: report does not contain status=pass")
  return
end
gralph.store.set("report_path", gralph.args.report)
```

## Working-directory note

A custom command runs in whatever directory the agent invoked it from; the
orchestrator launches the agent with cwd = the profile's directory. Relative
paths in `os.execute`/`io.open` therefore resolve against that dir unless the
agent changed directory. To be robust, have the agent submit paths as args and
validate them, or anchor commands to a known root, rather than assuming cwd.

## JSON parsing

gopher-lua has no built-in JSON decoder. Two robust options:

1. **Validate via an external tool** you already trust:
   `os.execute("jq -e '.failed == 0' " .. report .. " >/dev/null")` and check
   the exit code — `jq -e` exits non-zero if the filter is false/null.
2. **String/pattern checks** with `string.find` for simple, well-known shapes
   (as above). Keep these tight and anchored so the agent can't satisfy them
   trivially.

Prefer option 1 when the report has real structure; it's a true deterministic
gate that also documents the contract.
