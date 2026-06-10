-- plan: record the goal into the user store (used by later guidance).
local goal = gralph.args.goal
if goal == nil or #goal == 0 then
  gralph.fail("reason: --goal must not be empty")
  return
end
gralph.store.set("goal", goal)
gralph.store.set("attempts", 0)
