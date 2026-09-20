-- Lua debugger core.
--
-- Usage: LUA_DEVTOOLS_PROTOCOL=<session-file> lua debugger.lua <script.lua> [args...]
--
-- Talks to the Go debug adapter using stdin and a private protocol file, one JSON per line:
--   adapter -> here: commands  {"id":N,"cmd":"...", ...}
--   here -> adapter: replies   {"id":N,"body":...} / {"id":N,"error":"..."}
--                    events    {"event":"...", ...}
--
-- stdout/stderr are reserved for program output, including writes from C modules.
--
-- Pure Lua cannot read stdin without blocking, so commands are only read while paused
-- (breakpoint hit, step finished, or before the script starts).

local scriptDir = (arg and arg[0] or ""):match("^(.*)[/\\]") or "."
local json = dofile(scriptDir .. "/json.lua")

local SELF_SOURCE = debug.getinfo(1, "S").source

-------------------------------------------------------------------------------
-- Protocol I/O
-------------------------------------------------------------------------------

local protocolPath = assert(os.getenv("LUA_DEVTOOLS_PROTOCOL"), "missing debugger protocol path")
local protocol = assert(io.open(protocolPath, "ab"))
local stdin = io.stdin

local function send(msg)
  assert(protocol:write(json.encode(msg), "\n"))
  assert(protocol:flush())
end

local function output(category, text)
  send({ event = "output", category = category, text = text })
end

local function readMessage()
  while true do
    local line = stdin:read("*l")
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
local depth = 0        -- user-frame depth at the latest line hook
local inDebugger = false -- suppress hooks in coroutines resumed by evaluation
-- Weak registration does not keep otherwise unreachable coroutines alive.
local mainThread = coroutine.running()
local threadIDs = setmetatable({ [mainThread] = 1 }, { __mode = "k" })
local threads = setmetatable({ [1] = mainThread }, { __mode = "v" })
local nextThreadID = 1
local FRAME_STRIDE = 1000000
local function registerThread(co)
  if not threadIDs[co] then
    nextThreadID = nextThreadID + 1
    threadIDs[co] = nextThreadID
    threads[nextThreadID] = co
  end
  return threadIDs[co]
end

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

-- Suspended stacks have absolute levels; the active stack uses hook-relative
-- levels because commands add a different number of debugger frames.
local function frameLocation(frameId)
  if frameId < FRAME_STRIDE then
    return coroutine.running(), assert(findAnchor(), "no active stack") + frameId - 1
  end
  local co = assert(threads[math.floor(frameId / FRAME_STRIDE)], "unknown coroutine")
  assert(co ~= coroutine.running() and coroutine.status(co) ~= "dead", "stale frame")
  return co, frameId % FRAME_STRIDE
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

local function collectStack(threadId)
  local co = coroutine.running()
  if threadId then co = threads[threadId] end
  assert(co, "unknown coroutine")
  local active = co == coroutine.running()
  if coroutine.status(co) == "dead" then return {} end
  local anchor = active and findAnchor() or 0
  if not anchor then return {} end
  local frames = {}
  local level = anchor + 1
  while true do
    local info = debug.getinfo(co, level, "Slnf")
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
        id = active and (level - anchor) or (threadIDs[co] * FRAME_STRIDE + level),
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
  local co, level = frameLocation(frameId)
  local vars = {}
  local i = 1
  while true do
    local name, value = debug.getlocal(co, level, i)
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
  local co, level = frameLocation(frameId)
  local info = debug.getinfo(co, level, "f")
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
local function findLocal(co, level, name)
  if co == coroutine.running() then level = level + 1 end
  local found, foundIndex
  local i = 1
  while true do
    local n = debug.getlocal(co, level, i)
    if n == nil then
      break
    end
    if n == name then
      found, foundIndex = true, i
    end
    i = i + 1
  end
  if found then
    local _, v = debug.getlocal(co, level, foundIndex)
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
  local co, level = frameLocation(frameId)
  local ok, v = findLocal(co, level, name)
  if ok then
    return v
  end
  local info = debug.getinfo(co, level, "f")
  if info then
    ok, v = findUpvalue(info.func, name)
    if ok then
      return v
    end
  end
  return _G[name]
end

local function assignVariable(frameId, name, value)
  local co, level = frameLocation(frameId)
  local ok, _, index = findLocal(co, level, name)
  if ok then
    debug.setlocal(co, level, index, value)
    return
  end
  local info = debug.getinfo(co, level, "f")
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
    step = { mode = mode, depth = depth, thread = coroutine.running() }
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

function handlers.threads()
  local result = {}
  for id, co in pairs(threads) do
    if coroutine.status(co) ~= "dead" then
      result[#result + 1] = { id = id, name = id == 1 and "main" or ("coroutine " .. id .. " (" .. coroutine.status(co) .. ")") }
    end
  end
  table.sort(result, function(a, b) return a.id < b.id end)
  return { threads = result }
end

function handlers.stack(cmd)
  return { frames = collectStack(cmd.threadId) }
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
  inDebugger = true
  step = nil
  send({
    event = "stopped",
    threadId = registerThread(coroutine.running()),
    reason = reason,
    file = sourcePath(info.source),
    line = info.currentline,
    text = text,
  })
  commandLoop()
  inDebugger = false
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
    elseif step.mode == "over" and step.thread == coroutine.running() and depth <= step.depth then
      return "step"
    elseif step.mode == "out" and step.thread == coroutine.running() and depth < step.depth then
      return "step"
    end
  end
  return nil
end

local function stackDepth()
  local count = 0
  local level = 2
  while true do
    local frame = debug.getinfo(level, "S")
    if not frame then break end
    if frame.what ~= "C" and frame.source ~= SELF_SOURCE then
      count = count + 1
    end
    level = level + 1
  end
  return count
end

function hook(event, line)
  if inDebugger or event ~= "line" or (not step and next(breakpoints) == nil) then
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
  -- Derive depth from this coroutine's stack only when stepping needs it.
  -- Call/return counters drift on tail calls and across suspended coroutines.
  if step and step.mode ~= "in" then
    depth = stackDepth()
  end
  local reason = shouldStop(path, line)
  if reason then
    depth = stackDepth()
    info.currentline = line
    pause(reason, info)
  end
end

local function installCoroutineHooks()
  local create = coroutine.create
  local resumeCoroutine = coroutine.resume
  coroutine.create = function(fn)
    local co = create(fn)
    registerThread(co)
    debug.sethook(co, hook, "l")
    return co
  end
  coroutine.resume = function(co, ...)
    local result = table.pack(resumeCoroutine(co, ...))
    -- Stepping past yield or the end of a coroutine resumes in its caller.
    if step and step.thread == co then
      step = { mode = "in" }
    end
    return table.unpack(result, 1, result.n)
  end
  coroutine.wrap = function(fn)
    local co = coroutine.create(fn)
    return function(...)
      local result = table.pack(coroutine.resume(co, ...))
      if not result[1] then
        -- Lua 5.2/5.3 have neither coroutine.close nor to-be-closed variables.
        if coroutine.close then
          coroutine.close(co)
        end
        error(result[2], 2)
      end
      return table.unpack(result, 2, result.n)
    end
  end
end

-------------------------------------------------------------------------------
-- Startup
-------------------------------------------------------------------------------

local function finish(exitCode)
  debug.sethook()
  protocol:close()
  os.exit(exitCode, true)
end

local function main()
  local scriptPath = arg[1]
  if not scriptPath then
    io.stderr:write("usage: lua debugger.lua <script.lua> [args...]\n")
    os.exit(2)
  end
  local scriptArgs = { table.unpack(arg, 2) }

  -- Keep interactive output visible without replacing Lua file methods.
  io.stdout:setvbuf("no")
  io.stderr:setvbuf("no")

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
    installCoroutineHooks()
    debug.sethook(hook, "l")
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
