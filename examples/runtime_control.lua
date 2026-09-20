-- No dependencies. This deliberately busy loop consumes CPU; it is not sleep.
-- Start without breakpoints, click Pause, inspect counter/state, then Continue.
-- While running, add/remove a breakpoint on counter = counter + 1 below.
-- Try a conditional breakpoint: counter % 10000 == 0.
-- Native polling helper required for pause/live breakpoint updates.
local seconds = tonumber(arg[1] or "10")
assert(seconds and seconds > 0 and seconds <= 60, "seconds must be in (0, 60]")
local started = os.clock()
local counter = 0
local state = { phase = "running", checksum = 0 }

while os.clock() - started < seconds do
    counter = counter + 1
    state.checksum = (state.checksum + counter) % 65536
end

state.phase = "finished"
print(state.phase, "iterations", counter, "checksum", state.checksum)
-- os.clock measures CPU time: time spent paused does not use up the duration.
