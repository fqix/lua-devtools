-- Lua debugger core.
--
-- Usage: lua debugger.lua <script.lua> [args...]
--
-- Talks to the debug adapter (src/debug/runtime.ts) over stdin/stdout, one JSON per line:
--   adapter -> here: commands  {"id":N,"cmd":"...", ...}
--   here -> adapter: replies   {"id":N,"body":...} / {"id":N,"error":"..."}
--                    events    {"event":"...", ...}
--
-- stdout is the protocol channel, so print / io.write are replaced with output events.
-- A script writing to io.stdout directly will corrupt the protocol; known limitation.
--
-- Pure Lua cannot read stdin without blocking, so commands are only read while paused
-- (breakpoint hit, step finished, or before the script starts).

local scriptDir = (arg and arg[0] or ""):match("^(.*)[/\\]") or "."
local json = dofile(scriptDir .. "/json.lua")

local SELF_SOURCE = debug.getinfo(1, "S").source

-------------------------------------------------------------------------------
-- Protocol I/O
-------------------------------------------------------------------------------

local stdout = io.stdout

local function send(msg)
  stdout:write(json.encode(msg), "\n")
  stdout:flush()
end

local function output(category, text)
  send({ event = "output", category = category, text = text })
end

local function readMessage()
  while true do
    local line = io.read("l")
    if line == nil then
      return nil -- adapter closed stdin
    end
    if line ~= "" then
      local ok, msg = pcall(json.decode, line)
      if ok and type(msg) == "table" then
        return msg
      end
      output("stderr", "debugger: bad command line: " .. line .. "\n")
    end
  end
end

-- Commands that arrived while waiting for an adapter reply (see resolvePath).
local queuedCommands = {}

local function readCommand()
  if #queuedCommands > 0 then
    return table.remove(queuedCommands, 1)
  end
  return readMessage()
end

local adapterGone = false

-- Ask the adapter to canonicalize a path (absolute, symlinks resolved) so it
-- matches breakpoint paths. Pure Lua has no realpath; the adapter answers on
-- stdin with {"resolvedPath": ...}. Commands seen meanwhile are queued.
local function resolvePath(path)
  if adapterGone then
    return path
  end
  send({ event = "resolvePath", path = path })
  while true do
    local msg = readMessage()
    if msg == nil then
      adapterGone = true
      return path
    end
    if msg.resolvedPath ~= nil then
      return msg.resolvedPath
    end
    queuedCommands[#queuedCommands + 1] = msg
  end
end

-------------------------------------------------------------------------------
-- Debugger state
-------------------------------------------------------------------------------

local breakpoints = {} -- [absPath] = { [line] = { condition = string|nil } }
local step = nil       -- nil | { mode = "in"|"over"|"out", depth = number }
local depth = 0        -- call depth maintained from call/return hook events
local varRefs = {}     -- rebuilt on every pause: ref -> { kind = ..., ... }
local mainChunk = nil  -- the script's main function; stack listing stops there
local noDebug = false  -- "Run Without Debugging": never install the hook
local cwd = nil        -- working directory sent by the adapter, for relative sources

local hook    -- forward declaration; findAnchor locates it by identity
local onError -- xpcall message handler; the anchor while paused on an exception

local function newRef(entry)
  varRefs[#varRefs + 1] = entry
  return #varRefs
end

local function isAbsolute(path)
  return path:sub(1, 1) == "/" or path:match("^%a:[/\\]") ~= nil
end

-- Collapse "." and ".." segments so "@./mod.lua" and "@../x/./y.lua" match the
-- absolute paths the adapter uses for breakpoints.
local function normalizePath(path)
  local sep = path:find("\\", 1, true) and "\\" or "/"
  local parts = {}
  for segment in path:gmatch("[^/\\]+") do
    if segment == ".." then
      if #parts > 0 then
        parts[#parts] = nil
      end
    elseif segment ~= "." then
      parts[#parts + 1] = segment
    end
  end
  local prefix = path:match("^%a:") or ""
  if prefix ~= "" then
    table.remove(parts, 1)
  end
  return prefix .. sep .. table.concat(parts, sep)
end

local sourceCache = {}

-- Maps a chunk's `source` ("@path") to the canonical path the adapter uses for
-- breakpoints. Modules found through relative package.path entries report
-- "@./mod.lua"; the adapter also resolves symlinks. Cached per source.
local function sourcePath(source)
  local cached = sourceCache[source]
  if cached ~= nil then
    return cached or nil
  end
  local path = nil
  if source:sub(1, 1) == "@" then
    path = source:sub(2)
    if not isAbsolute(path) and cwd then
      path = cwd .. "/" .. path
    end
    path = resolvePath(normalizePath(path))
  end
  sourceCache[source] = path or false
  return path
end

-- Return the level of the anchor frame (hook or onError) as seen by the caller.
-- User frame i lives at anchor + i.
local function findAnchor()
  local level = 2 -- 1 = findAnchor itself, 2 = caller
  while true do
    local info = debug.getinfo(level, "f")
    if not info then
      return nil
    end
    if info.func == hook or info.func == onError then
      return level - 1 -- the caller is one level closer to the anchor
    end
    level = level + 1
  end
end

-------------------------------------------------------------------------------
-- Variable serialization
-------------------------------------------------------------------------------

local function formatValue(v)
  local t = type(v)
  if t == "string" then
    return (string.format("%q", v):gsub("\\\n", "\\n"))
  end
  return tostring(v)
end

-- Build a DAP Variable; tables get a ref so they can be expanded later.
local function makeVariable(name, value)
  local t = type(value)
  local ref = 0
  if t == "table" then
    ref = newRef({ kind = "table", value = value })
  end
  return { name = name, value = formatValue(value), type = t, variablesReference = ref }
end

local function collectStack()
  local anchor = findAnchor()
  if not anchor then
    return {}
  end
  local frames = {}
  local level = anchor + 1
  while true do
    local info = debug.getinfo(level, "Slnf")
    if not info then
      break
    end
    local path = sourcePath(info.source)
    if path and info.source ~= SELF_SOURCE then
      local name = info.name
      if info.func == mainChunk then
        name = "main chunk"
      elseif not name then
        name = "<anonymous:" .. tostring(info.linedefined) .. ">"
      end
      frames[#frames + 1] = {
        id = level - anchor,
        name = name,
        file = path,
        line = info.currentline,
      }
    end
    if info.func == mainChunk then
      break
    end
    level = level + 1
  end
  return frames
end

local function collectLocals(frameId)
  local anchor = findAnchor()
  local level = anchor + frameId
  local vars = {}
  local i = 1
  while true do
    local name, value = debug.getlocal(level, i)
    if name == nil then
      break
    end
    if name:sub(1, 1) ~= "(" then
      vars[#vars + 1] = makeVariable(name, value)
    end
    i = i + 1
  end
  return vars
end

local function collectUpvalues(frameId)
  local anchor = findAnchor()
  local info = debug.getinfo(anchor + frameId, "f")
  local vars = {}
  if not info then
    return vars
  end
  local i = 1
  while true do
    local name, value = debug.getupvalue(info.func, i)
    if name == nil then
      break
    end
    vars[#vars + 1] = makeVariable(name, value)
    i = i + 1
  end
  return vars
end

local MAX_TABLE_ITEMS = 200

local function formatKey(k)
  if type(k) == "string" and k:match("^[%a_][%w_]*$") then
    return k
  end
  return "[" .. formatValue(k) .. "]"
end

local function collectTable(t)
  local keys = {}
  for k in pairs(t) do
    keys[#keys + 1] = k
  end
  table.sort(keys, function(a, b)
    local ta, tb = type(a), type(b)
    if ta ~= tb then
      return ta < tb
    end
    if ta == "number" or ta == "string" then
      return a < b
    end
    return tostring(a) < tostring(b)
  end)
  local vars = {}
  for i, k in ipairs(keys) do
    if i > MAX_TABLE_ITEMS then
      vars[#vars + 1] = { name = "...", value = ("(%d more)"):format(#keys - MAX_TABLE_ITEMS), type = "", variablesReference = 0 }
      break
    end
    vars[#vars + 1] = makeVariable(formatKey(k), t[k])
  end
  return vars
end

-------------------------------------------------------------------------------
-- Expression evaluation: expressions see the frame's locals, upvalues and globals
-------------------------------------------------------------------------------

-- `level` is relative to the caller of findLocal (so +1 inside here).
-- The last local with the given name wins: inner scopes shadow outer ones.
local function findLocal(level, name)
  level = level + 1
  local found, foundIndex
  local i = 1
  while true do
    local n = debug.getlocal(level, i)
    if n == nil then
      break
    end
    if n == name then
      found, foundIndex = true, i
    end
    i = i + 1
  end
  if found then
    local _, v = debug.getlocal(level, foundIndex)
    return true, v, foundIndex
  end
  return false
end

local function findUpvalue(func, name)
  local i = 1
  while true do
    local n, v = debug.getupvalue(func, i)
    if n == nil then
      return false
    end
    if n == name then
      return true, v, i
    end
    i = i + 1
  end
end

local function lookupVariable(frameId, name)
  local anchor = findAnchor()
  local level = anchor + frameId
  local ok, v = findLocal(level, name)
  if ok then
    return v
  end
  local info = debug.getinfo(level, "f")
  if info then
    ok, v = findUpvalue(info.func, name)
    if ok then
      return v
    end
  end
  return _G[name]
end

local function assignVariable(frameId, name, value)
  local anchor = findAnchor()
  local level = anchor + frameId
  local ok, _, index = findLocal(level, name)
  if ok then
    debug.setlocal(level, index, value)
    return
  end
  local info = debug.getinfo(level, "f")
  if info then
    ok, _, index = findUpvalue(info.func, name)
    if ok then
      debug.setupvalue(info.func, index, value)
      return
    end
  end
  _G[name] = value
end

local function makeEnv(frameId)
  return setmetatable({}, {
    __index = function(_, name)
      return lookupVariable(frameId, name)
    end,
    __newindex = function(_, name, value)
      assignVariable(frameId, name, value)
    end,
  })
end

-- Evaluate in the given frame. Compile as an expression first, then as a statement.
-- Returns ok, results (packed table), n
local function evaluateIn(frameId, expression)
  local env = makeEnv(frameId)
  local fn, err = load("return " .. expression, "=(eval)", "t", env)
  if not fn then
    fn, err = load(expression, "=(eval)", "t", env)
  end
  if not fn then
    return false, err
  end
  local results = table.pack(pcall(fn))
  if not results[1] then
    return false, results[2]
  end
  return true, results, results.n - 1
end

-------------------------------------------------------------------------------
-- Command handlers
-------------------------------------------------------------------------------

local RESUME = {}

local function setBreakpoints(cmd)
  local file = assert(cmd.file, "setBreakpoints needs file")
  local list = {}
  local verified = {}
  for _, bp in ipairs(cmd.breakpoints or {}) do
    list[bp.line] = { condition = bp.condition }
    verified[#verified + 1] = { line = bp.line, verified = true }
  end
  breakpoints[file] = next(list) and list or nil
  return { breakpoints = verified }
end

local function resume(mode)
  if mode then
    step = { mode = mode, depth = depth }
  else
    step = nil
  end
  varRefs = {}
  return RESUME
end

local handlers = {}

handlers.setBreakpoints = setBreakpoints

function handlers.run(cmd)
  noDebug = cmd.noDebug and true or false
  cwd = cmd.cwd
  return resume(cmd.stopOnEntry and "in" or nil)
end

function handlers.continue()
  return resume(nil)
end

function handlers.next()
  return resume("over")
end

function handlers.stepIn()
  return resume("in")
end

function handlers.stepOut()
  return resume("out")
end

function handlers.stack()
  return { frames = collectStack() }
end

function handlers.scopes(cmd)
  local frameId = assert(cmd.frameId, "scopes needs frameId")
  return {
    scopes = {
      { name = "Locals", variablesReference = newRef({ kind = "locals", frameId = frameId }) },
      { name = "Upvalues", variablesReference = newRef({ kind = "upvalues", frameId = frameId }) },
    },
  }
end

function handlers.variables(cmd)
  local entry = varRefs[cmd.ref or 0]
  if not entry then
    error("unknown variablesReference " .. tostring(cmd.ref))
  end
  if entry.kind == "locals" then
    return { variables = collectLocals(entry.frameId) }
  elseif entry.kind == "upvalues" then
    return { variables = collectUpvalues(entry.frameId) }
  elseif entry.kind == "table" then
    return { variables = collectTable(entry.value) }
  end
  error("cannot expand " .. entry.kind)
end

function handlers.evaluate(cmd)
  local expression = assert(cmd.expression, "evaluate needs expression")
  local frameId = cmd.frameId or 1
  local ok, results, n = evaluateIn(frameId, expression)
  if not ok then
    error(tostring(results), 0)
  end
  if n == 0 then
    return { result = "nil", type = "nil", variablesReference = 0 }
  end
  if n == 1 then
    local var = makeVariable("", results[2])
    return { result = var.value, type = var.type, variablesReference = var.variablesReference }
  end
  local parts = {}
  for i = 2, n + 1 do
    parts[#parts + 1] = formatValue(results[i])
  end
  return { result = table.concat(parts, ", "), type = "multiple", variablesReference = 0 }
end

-- Read and answer commands (blocking) until one resumes execution.
local function commandLoop()
  while true do
    local cmd = readCommand()
    if not cmd then
      os.exit(0, true)
    end
    local handler = handlers[cmd.cmd]
    local reply = { id = cmd.id }
    local result
    if not handler then
      reply.error = "unknown command: " .. tostring(cmd.cmd)
    else
      local ok, res = pcall(handler, cmd)
      if ok then
        result = res
        if res ~= RESUME then
          reply.body = res
        end
      else
        reply.error = tostring(res)
      end
    end
    if cmd.id ~= nil then
      send(reply)
    end
    if result == RESUME then
      return
    end
  end
end

local function pause(reason, info, text)
  step = nil
  send({
    event = "stopped",
    reason = reason,
    file = sourcePath(info.source),
    line = info.currentline,
    text = text,
  })
  commandLoop()
end

-------------------------------------------------------------------------------
-- hook
-------------------------------------------------------------------------------

local function shouldStop(path, line)
  local fileBps = breakpoints[path]
  local bp = fileBps and fileBps[line]
  if bp then
    if not bp.condition then
      return "breakpoint"
    end
    -- The condition is evaluated in the frame that hit the breakpoint (frame 1).
    -- A condition that fails to evaluate still stops, with a message.
    local ok, results, n = evaluateIn(1, bp.condition)
    if not ok then
      output("stderr", ("breakpoint condition failed at %s:%d: %s\n"):format(path, line, tostring(results)))
      return "breakpoint"
    end
    if n >= 1 and results[2] then
      return "breakpoint"
    end
  end
  if step then
    if step.mode == "in" then
      return "step"
    elseif step.mode == "over" and depth <= step.depth then
      return "step"
    elseif step.mode == "out" and depth < step.depth then
      return "step"
    end
  end
  return nil
end

function hook(event, line)
  if event == "call" then
    depth = depth + 1
    return
  elseif event == "return" then
    depth = depth - 1
    return
  elseif event ~= "line" then
    return
  end

  if not step and next(breakpoints) == nil then
    return
  end

  local info = debug.getinfo(2, "S")
  if info.source == SELF_SOURCE then
    return
  end
  local path = sourcePath(info.source)
  if not path then
    return
  end
  local reason = shouldStop(path, line)
  if reason then
    info.currentline = line
    pause(reason, info)
  end
end

-------------------------------------------------------------------------------
-- Startup
-------------------------------------------------------------------------------

local function installOutputCapture()
  _G.print = function(...)
    local n = select("#", ...)
    local parts = {}
    for i = 1, n do
      parts[i] = tostring((select(i, ...)))
    end
    output("stdout", table.concat(parts, "\t") .. "\n")
  end
  io.write = function(...)
    local parts = {}
    for i = 1, select("#", ...) do
      parts[i] = tostring((select(i, ...)))
    end
    output("stdout", table.concat(parts))
  end
end

local function finish(exitCode)
  debug.sethook()
  send({ event = "exited", exitCode = exitCode })
  os.exit(exitCode, true)
end

local function main()
  local scriptPath = arg[1]
  if not scriptPath then
    io.stderr:write("usage: lua debugger.lua <script.lua> [args...]\n")
    os.exit(2)
  end
  local scriptArgs = { table.unpack(arg, 2) }

  installOutputCapture()

  -- Wait for the adapter to send breakpoints and `run` before executing anything.
  commandLoop()

  local chunk, err = loadfile(scriptPath)
  if not chunk then
    output("stderr", tostring(err) .. "\n")
    finish(1)
  end
  mainChunk = chunk

  -- Make `arg` look the same as when running the script with lua directly.
  local newArg = { [0] = scriptPath }
  for i, v in ipairs(scriptArgs) do
    newArg[i] = v
  end
  newArg[-1] = arg[-1]
  _G.arg = newArg

  function onError(msg)
    -- The stack is still intact here. Disable the hook, then pause so the
    -- user can inspect variables at the error site.
    debug.sethook()
    local tb = debug.traceback(tostring(msg), 2)
    -- Drop xpcall and the debugger's own frames below it.
    tb = tb:gsub("\n\t%[C%]: in function 'xpcall'.*$", "")
    -- Pause at the user frame closest to the error.
    local level = 2
    while true do
      local info = debug.getinfo(level, "Sl")
      if not info then
        break
      end
      if sourcePath(info.source) and info.source ~= SELF_SOURCE then
        -- Without debugging, just report the traceback and exit.
        if not noDebug then
          pause("exception", info, tostring(msg))
        end
        break
      end
      level = level + 1
    end
    return tb
  end

  if not noDebug then
    debug.sethook(hook, "crl")
  end
  local ok, traceback = xpcall(chunk, onError, table.unpack(scriptArgs))
  debug.sethook()

  if not ok then
    output("stderr", traceback .. "\n")
    finish(1)
  end
  finish(0)
end

main()
