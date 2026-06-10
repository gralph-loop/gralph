-- Gate (format validation): tag must be strict semver vMAJOR.MINOR.PATCH.
local t = gralph.args.tag
if t == nil or not string.match(t, "^v%d+%.%d+%.%d+$") then
  gralph.fail("reason: --tag must be semver like v1.2.3, got '" .. tostring(t) .. "'")
  return
end
gralph.store.set("released", t)
