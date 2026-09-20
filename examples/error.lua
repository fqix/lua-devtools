local function divide(a, b)
    if b == 0 then
        error("division by zero")
    end
    return a / b
end

local values = { 10, 5, 0 }
for _, v in ipairs(values) do
    print("10 /", v, "=", divide(10, v))
end
