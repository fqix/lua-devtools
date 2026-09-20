-- Break on coroutine.yield or on total = total + item.value.
-- While paused in the producer, inspect its locals and the coroutine list.
local producer = coroutine.create(function(count)
    for index = 1, count do
        local item = { index = index, value = index * index }
        coroutine.yield(item)
    end
    return "finished"
end)

local total = 0
local ok, item = coroutine.resume(producer, 4)
while coroutine.status(producer) ~= "dead" do
    assert(ok, item)
    total = total + item.value
    print("consumed", item.index, item.value, "total", total)
    ok, item = coroutine.resume(producer)
end
assert(ok, item)
assert(total == 30)
print(item, "total", total, "status", coroutine.status(producer))

-- wrap returns yielded values directly and turns coroutine errors into errors.
local nextValue = coroutine.wrap(function()
    coroutine.yield("first")
    return "last"
end)
print("wrapped", nextValue(), nextValue())
