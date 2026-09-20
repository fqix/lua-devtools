-- require("lib.money") finds this directory module via ?/init.lua.
local money = {}

function money.format(cents)
    local whole = math.floor(cents / 100) -- F11 from cart.checkout enters this file.
    local fraction = cents % 100
    return string.format("%d.%02d", whole, fraction)
end

return money
