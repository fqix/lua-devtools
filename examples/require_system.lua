-- Use the interpreter's existing package.path/package.cpath; no local path changes.
-- LuaSocket must be installed for this interpreter. No network requests are made.
local mathlib = require("math") -- Built-in library, already in package.loaded.
local socket = require("socket") -- Installed Lua wrapper plus a native C module.
local url = require("socket.url") -- Installed Lua source: F11 can enter url.lua.

local root = mathlib.sqrt(81) -- C function: cannot step into its native implementation.
assert(root == 9)

-- Break here, then F11. Inspect parsed and use the call stack to navigate back.
local parsed = url.parse("https://example.com/docs/lua?version=5.4")
assert(parsed.scheme == "https" and parsed.host == "example.com")
assert(parsed.path == "/docs/lua" and parsed.query == "version=5.4")
print("URL", parsed.scheme, parsed.host, parsed.path, parsed.query)

local timestamp = socket.gettime() -- C function: stops before/after the call only.
print("sqrt", root, "time", timestamp)
print("loaded socket", socket._VERSION)
