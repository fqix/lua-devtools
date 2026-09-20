-- resume captures errors: no exception stop occurs inside the failed coroutine.
-- To inspect its locals before failure, put a breakpoint on error(...) below.
local task = coroutine.create(function()
    local job = { id = 42, state = "processing" }
    coroutine.yield(job.state)
    error("job " .. job.id .. " failed")
end)

local ok, result = coroutine.resume(task)
assert(ok and result == "processing")
print("yielded", result)

ok, result = coroutine.resume(task)
assert(not ok)
print("captured error", result)
print("coroutine status", coroutine.status(task))
-- The captured error does not fail this script; the caller chooses how to handle it.
