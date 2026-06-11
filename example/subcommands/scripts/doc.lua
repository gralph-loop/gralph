-- Single-slot subcommand (count 1, no key): verify the manual exists.
local path = gralph.args.doc or "out/manual.md"
local f = io.open(path, "r")
if not f then
  gralph.fail("reason: " .. path .. " missing; write the manual first")
  return
end
f:close()
gralph.store.set("doc_path", path)
