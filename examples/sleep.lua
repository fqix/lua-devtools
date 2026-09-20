-- Requires LuaSocket for the selected interpreter: luarocks install luasocket
-- Break before/after socket.sleep. Pause requests wait until the C call returns.
local socket = require("socket")
local seconds = tonumber(arg[1] or "2")
assert(seconds and seconds >= 0 and seconds <= 30, "seconds must be between 0 and 30")

for iteration = 1, 3 do
    local started = socket.gettime()
    print("before sleep", iteration, seconds)
    socket.sleep(seconds) -- Blocking: no Lua debug hooks run inside this call.
    local elapsed = socket.gettime() - started -- Break here to inspect elapsed.
    print("after sleep", iteration, elapsed)
end
