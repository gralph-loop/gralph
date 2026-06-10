local s = gralph.args.summary
if s == nil or #s < 3 then
  gralph.fail("reason: summary too short")
  return
end
gralph.store.set("summary", s)
