-- Per-item gate: the artifact for THIS key must exist and be non-empty.
-- Runs outside the state lock, so heavy checks here still execute in
-- parallel across workers.
local part = gralph.args.part
local path = "out/" .. part .. ".txt"
local f = io.open(path, "r")
if not f then
  gralph.fail("reason: " .. path .. " missing; create it before running make-part")
  return
end
local body = f:read("*a")
f:close()
if body == nil or #body == 0 then
  gralph.fail("reason: " .. path .. " is empty; write the part's content first")
  return
end
-- Evidence is namespaced by the work-item key so parallel workers never
-- collide (commits merge per key).
gralph.store.set("part:" .. part, #body)
