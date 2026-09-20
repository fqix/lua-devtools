# Debugging modes

Ordinary F5 launch needs only Lua. Interactive launch and cooperative attach additionally need LuaSocket installed for the interpreter used by the program (the environment tree can install `luasocket`).

## Program input

Add `"interactive": true` to a Lua launch configuration. Use **Lua DevTools: Send Program Input** from the Command Palette to send a line, or **Lua DevTools: Close Program Input** to send EOF. Input goes to the program's stdin; the debugger uses a separate authenticated loopback TCP connection. This supports `io.read`, not terminal emulation or raw keyboard input. EOF cannot be reversed during that session.

## Attach and embedded hosts

Run **Lua DevTools: Copy Attach Bootstrap** to copy a snippet containing this installation's debugger path. Add it to the host before the code to debug:

```lua
local session = dofile("/absolute/path/to/extension/lua/lua-devtools.lua").listen {
  host = "127.0.0.1",
  port = 8172,
  token = "my-session-token",
  timeout = 120,
}
-- Code here runs after the adapter connects and configures breakpoints.
local function work()
  local value = 42
  return value
end
local result = session:run(work) -- optional: inspect errors before unwinding
session.stop()                -- restore previous hooks and coroutine APIs
```

Start the host, then use this VS Code configuration:

```json
{
  "type": "lua",
  "request": "attach",
  "name": "Attach to Lua",
  "host": "127.0.0.1",
  "port": 8172,
  "token": "my-session-token",
  "cwd": "${workspaceFolder}",
  "stopOnEntry": false
}
```

The host owns its Lua module paths, stdout and stdin. Source paths must be accessible to VS Code and match the host's paths; remote path mapping is not provided. `listen` waits for one adapter, with a default 120-second timeout. Disconnect/Stop detaches and lets the host continue; it does not kill the host. Hooks and coroutine APIs are restored when Lua next observes the disconnect. A blocking C call must return first.

Attach is cooperative: an embedding application must load the bootstrap in the Lua state being debugged and provide the standard `debug`, `io`, `os`, `package` libraries and LuaSocket. It does not attach by process ID or debug C code. `session:run` preserves return values and rethrows errors after inspection; wrap it in `pcall` if the host should recover. Direct host code still supports breakpoints and stepping, but use `session:run` to inspect uncaught errors before its stack unwinds.

Non-loopback listeners require a token. The token is sent over plain TCP; use loopback or a trusted encrypted tunnel for remote use.

## Coroutines

While paused, **Lua DevTools: Resume Suspended Coroutine** runs a selected suspended coroutine without resuming its caller. It supplies no resume arguments and discards yielded/returned values, so use it deliberately: manual resume changes program flow. The debugger stops again when it yields or finishes, or at an intervening breakpoint. Normal continue and stepping retain the program's own scheduler behavior.

Set `"breakOnCoroutineErrors": true` in launch or attach to inspect a failed coroutine after `coroutine.resume` catches its error, before the caller receives the failure result. Its retained stack and locals are available until continuing. The default is false. As with Lua itself, this does not rewind a failed coroutine or restart it.

## Complex table views

Ordinary tables keep ordinary JSON shape. Tables that require richer representation use `{"$format":"lua-table-v1","root":...}`. Each table has `$id`, `$type: "table"` and `entries` containing key/value pairs; repeated objects use `$ref`. Binary strings use `$type: "bytes"` and hexadecimal data; non-finite numbers use tagged names. This also preserves sparse numeric keys, mixed keys and object keys without stringifying them. The view is read-only and has no export command.
