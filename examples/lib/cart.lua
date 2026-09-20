local money = require("lib.money")
local cart = {}

function cart.checkout(items)
    local total = 0
    local lines = {}
    for _, item in ipairs(items) do
        local subtotal = item.price * item.quantity -- Break here; inspect item/subtotal.
        total = total + subtotal
        lines[#lines + 1] = item.name .. ": " .. money.format(subtotal)
    end
    return { total = total, display = money.format(total), lines = lines }
end

return cart
