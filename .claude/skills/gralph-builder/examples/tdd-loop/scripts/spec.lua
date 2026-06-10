-- Gate: feature name must be a non-empty single token.
local f = gralph.args.feature
if f == nil or #f == 0 then
  gralph.fail("reason: --feature must not be empty")
  return
end
if string.find(f, "%s") then
  gralph.fail("reason: --feature must be a single token (no spaces)")
  return
end
gralph.store.set("feature", f)
gralph.store.set("passes", 0)
