-- Finalize gate: aggregate verification over the completed work items.
-- gralph.progress.* is available here (and only here).
local parts = gralph.progress.keys("make-part")
if gralph.progress.count("make-part") < 3 then
  gralph.fail("reason: expected 3 parts, progress has " .. gralph.progress.count("make-part"))
  return
end
-- Re-check every recorded artifact still exists (a worker could have been
-- verified and the file deleted since).
for _, p in ipairs(parts) do
  local f = io.open("out/" .. p .. ".txt", "r")
  if not f then
    gralph.fail("reason: out/" .. p .. ".txt vanished after verification; recreate it")
    return
  end
  f:close()
end
gralph.store.set("parts_csv", table.concat(parts, ", "))
