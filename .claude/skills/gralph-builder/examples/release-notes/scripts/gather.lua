-- Gate: source must exist and contain at least one real PR ref (#NNN).
-- Record the PR count so later guidance/gates can use it.
local src = gralph.args.source
local f = io.open(src, "r")
if f == nil then
  gralph.fail("reason: source '" .. src .. "' was not created")
  return
end
local body = f:read("*a"); f:close()
local count = 0
for _ in string.gmatch(body, "#%d+") do count = count + 1 end
if count == 0 then
  gralph.fail("reason: source has no PR references like #123; add them")
  return
end
gralph.store.set("source", src)
gralph.store.set("pr_count", count)
