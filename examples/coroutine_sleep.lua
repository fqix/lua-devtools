-- Requires LuaSocket. Cooperative sleep yields a delay to the scheduler.
-- Break inside worker: the other worker should appear as a suspended coroutine.
local socket = require("socket")
local tasks = {}

local function sleep(seconds)
    coroutine.yield(seconds)
end

local function worker(name, delay)
    for iteration = 1, 3 do
        print(name, "working", iteration)
        sleep(delay) -- Only this worker waits; the scheduler can resume others.
    end
    print(name, "done")
end

local function spawn(name, delay)
    tasks[#tasks + 1] = {
        thread = coroutine.create(function() worker(name, delay) end),
        wakeAt = socket.gettime(),
    }
end

spawn("fast", 0.2)
spawn("slow", 0.5)

while #tasks > 0 do
    local nextWake = math.huge
    for index = #tasks, 1, -1 do
        local task = tasks[index]
        if socket.gettime() >= task.wakeAt then
            local ok, delay = coroutine.resume(task.thread)
            assert(ok, delay)
            if coroutine.status(task.thread) == "dead" then
                table.remove(tasks, index)
            else
                task.wakeAt = socket.gettime() + delay
            end
        end
        if coroutine.status(task.thread) ~= "dead" then
            nextWake = math.min(nextWake, task.wakeAt)
        end
    end
    if #tasks > 0 then
        -- Limit each blocking C sleep to 50 ms so Lua hooks run regularly.
        socket.sleep(math.max(0, math.min(0.05, nextWake - socket.gettime())))
    end
end
print("all workers finished")
