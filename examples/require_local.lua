-- Works from the repository root, examples/, or VS Code's current-file launch.
-- Resolve local modules relative to this script, preserving installed search paths.
local scriptDir = (arg[0]:match("^(.*)[/\\]") or ".")
package.path = scriptDir .. "/?.lua;" .. scriptDir .. "/?/init.lua;" .. package.path

local cart = require("lib.cart")
local money = require("lib.money")
assert(cart == require("lib.cart")) -- require caches the module in package.loaded.

local items = {
    { name = "notebook", price = 1200, quantity = 2 },
    { name = "pen", price = 300, quantity = 3 },
}

-- Put a breakpoint here, then F11 into lib/cart.lua and lib/money/init.lua.
local receipt = cart.checkout(items)
assert(receipt.total == 3300)
assert(receipt.display == "33.00")
print("total", receipt.total, "formatted", money.format(receipt.total))

for _, line in ipairs(receipt.lines) do
    print(line)
end
