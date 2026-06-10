-- Gate: require a non-trivial note (audit trail). The real correctness check
-- happens back in verify, which re-runs ./check.sh.
local n = gralph.args.note
if n == nil or #n < 3 then
  gralph.fail("reason: --note must describe the fix (>= 3 chars)")
  return
end
gralph.store.set("last_fix", n)
