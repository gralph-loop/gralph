-- Gate: version must be strict semver vMAJOR.MINOR.PATCH.
local v = gralph.args.version
if v == nil or not string.match(v, "^v%d+%.%d+%.%d+$") then
  gralph.fail("reason: --version must be semver like v2.3.0, got '" .. tostring(v) .. "'")
  return
end
gralph.store.set("published", v)
