-- Gate (structured-evidence cross-check): every #NNN cited in the notes must
-- exist in the source. Catches fabricated/hallucinated citations. The agent
-- cannot pass this by asserting anything; the check reads both files.
local source = gralph.store.get("source")
local notes  = gralph.store.get("notes")

-- Build a lookup of PR refs that really exist in the source.
local sf = io.open(source, "r")
if sf == nil then gralph.fail("reason: source file missing: " .. tostring(source)); return end
local sbody = sf:read("*a"); sf:close()
local exists = {}
for pr in string.gmatch(sbody, "#%d+") do exists[pr] = true end

-- Check each citation in the notes.
local nf = io.open(notes, "r")
if nf == nil then gralph.fail("reason: notes file missing: " .. tostring(notes)); return end
local nbody = nf:read("*a"); nf:close()

local fabricated = {}
local cited = 0
for pr in string.gmatch(nbody, "#%d+") do
  cited = cited + 1
  if not exists[pr] then table.insert(fabricated, pr) end
end

if #fabricated > 0 then
  gralph.fail("reason: notes cite PRs not in the source: " ..
              table.concat(fabricated, ", ") .. " — remove or correct them")
  return
end
gralph.store.set("cited_prs", cited)
