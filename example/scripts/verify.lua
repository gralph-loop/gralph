-- verify: route between fix and finish. First pass always routes to fix
-- (simulating a found defect); second pass accepts and finishes.
local attempts = (gralph.store.get("attempts") or 0) + 1
gralph.store.set("attempts", attempts)

if gralph.args.ok ~= "yes" then
  gralph.fail("reason: verification not confirmed; rerun with --ok yes")
  return
end

if attempts < 2 then
  gralph.route("fix")
else
  gralph.route("finish")
end
