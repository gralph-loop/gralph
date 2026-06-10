-- implement: deterministic verification = the report file must exist.
local path = gralph.args.report
local f = io.open(path, "r")
if f == nil then
  gralph.fail("reason: report file '" .. path .. "' does not exist; create it and resubmit")
  return
end
f:close()
gralph.store.set("report_path", path)
