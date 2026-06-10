-- Gate (run-the-checker + route): execute the project's check and route on the
-- REAL exit code. The agent cannot fake a passing check.
local passes = (gralph.store.get("passes") or 0) + 1
gralph.store.set("passes", passes)
if os.execute("sh ./check.sh >/dev/null 2>&1") == 0 then
  gralph.route("release")
else
  gralph.route("fix")
end
