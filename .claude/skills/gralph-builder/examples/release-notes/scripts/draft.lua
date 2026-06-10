-- Gate: notes must exist, be non-trivial, and cite at least one PR.
local notes = gralph.args.notes
local f = io.open(notes, "r")
if f == nil then gralph.fail("reason: notes '" .. notes .. "' was not created"); return end
local body = f:read("*a"); f:close()
if #body < 40 then
  gralph.fail("reason: notes too short (" .. #body .. " bytes); flesh them out")
  return
end
if not string.find(body, "#%d+") then
  gralph.fail("reason: notes cite no PR numbers (#NNN); cite the changes")
  return
end
gralph.store.set("notes", notes)
