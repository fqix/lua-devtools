-- Snapshot policy and a tagged fallback for values that ordinary JSON cannot model.
-- json.lua remains the only encoder. Traversal never invokes debuggee metamethods.
local encode = ...
local options = {
  emptyTableAsObject = true, preciseNumbers = true, validateUTF8 = true,
  sortKeys = true, unsupportedAsString = true,
  maxDepth = 32, maxValues = 10000, maxBytes = 512 * 1024,
}

return function(value)
  local ok, result = pcall(encode, value, options)
  if ok then return result end
  if not (result:find('circular', 1, true) or result:find('string keys', 1, true) or
          result:find('UTF-8', 1, true) or result:find('infinity', 1, true)) then error(result, 0) end
  local identities, serial, count = {}, 0, 0
  local function visit(v, depth)
    count = count + 1
    if count > options.maxValues then error('Snapshot exceeds 10000 values') end
    if depth > options.maxDepth then error('Snapshot exceeds 32 levels') end
    local kind = type(v)
    if kind == 'number' then
      if v ~= v then return {['$type']='number', value='NaN'} end
      if v == math.huge then return {['$type']='number', value='+Infinity'} end
      if v == -math.huge then return {['$type']='number', value='-Infinity'} end
      return v
    elseif kind == 'string' then
      local valid, err = pcall(encode, v, {validateUTF8=true, maxBytes=options.maxBytes})
      if valid then return v end
      if not err:find('UTF-8', 1, true) then error(err, 0) end
      return {['$type']='bytes', hex=(v:gsub('.', function(c) return string.format('%02x', c:byte()) end))}
    elseif kind == 'nil' or kind == 'boolean' then return v end
    if identities[v] then return {['$ref']=identities[v]} end
    serial = serial + 1
    identities[v] = serial
    local tagged = {['$id']=serial, ['$type']=kind}
    if kind ~= 'table' then return tagged end
    local keys = {}
    for key in next, v do
      if #keys >= options.maxValues then error('Snapshot exceeds 10000 table entries') end
      keys[#keys+1] = key
    end
    table.sort(keys, function(a, b)
      local ta, tb = type(a), type(b)
      if ta ~= tb then return ta < tb end
      if ta == 'string' or ta == 'number' then return a < b end
      if ta == 'boolean' then return not a and b end
      return false
    end)
    tagged.entries = {}
    for _, key in ipairs(keys) do
      tagged.entries[#tagged.entries+1] = {key=visit(key, depth+1), value=visit(rawget(v,key), depth+1)}
    end
    return tagged
  end
  local extended = {['$format']='lua-table-v1', root=visit(value, 0)}
  -- Metadata adds levels/values; the original graph was bounded during traversal.
  return encode(extended, {preciseNumbers=true, validateUTF8=true, sortKeys=true, maxDepth=100, maxValues=80000, maxBytes=options.maxBytes})
end
