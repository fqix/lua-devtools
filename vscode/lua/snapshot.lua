-- Debugger snapshot policy; encoding lives in json.lua.
local encode = ...
local options = {
  emptyTableAsObject = true,
  preciseNumbers = true,
  validateUTF8 = true,
  sortKeys = true,
  unsupportedAsString = true,
  maxDepth = 32,
  maxValues = 10000,
  maxBytes = 512 * 1024,
}

return function(value)
  return encode(value, options)
end
