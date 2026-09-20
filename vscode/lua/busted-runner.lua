-- Run Busted in this Lua process so ordinary Lua breakpoints keep working.
-- CLI arguments are supplied by the extension; no user text is evaluated here.
local ok, runner = pcall(require, "busted.runner")
if not ok then
  io.stderr:write("Busted is unavailable for the selected Lua interpreter. Install busted for this Lua version.\n", tostring(runner), "\n")
  os.exit(1)
end
runner({ standalone = false })
