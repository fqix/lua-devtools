local function square(n)
    return n * n
end

local function sumSquares(list)
    local total = 0
    for i, v in ipairs(list) do
        total = total + square(v)
    end
    return total
end

local numbers = { 1, 2, 3, 4 }
local config = { name = "demo", nested = { depth = 2 }, [10] = true }
local result = sumSquares(numbers)
print("sum of squares:", result, config.name)
