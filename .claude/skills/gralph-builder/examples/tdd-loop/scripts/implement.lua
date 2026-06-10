-- Gate (artifact-exists): every path the agent claims to have written must exist.
local files = gralph.args.files
local missing = {}
for path in string.gmatch(files, "[^,]+") do
  path = path:gsub("^%s+", ""):gsub("%s+$", "")
  if #path > 0 and io.open(path, "r") == nil then
    table.insert(missing, path)
  end
end
if #missing > 0 then
  gralph.fail("reason: these claimed files do not exist: " .. table.concat(missing, ", "))
  return
end
gralph.store.set("files", files)
