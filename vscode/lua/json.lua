--
-- json.lua
--
-- Copyright (c) 2020 rxi
--
-- Permission is hereby granted, free of charge, to any person obtaining a copy of
-- this software and associated documentation files (the "Software"), to deal in
-- the Software without restriction, including without limitation the rights to
-- use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies
-- of the Software, and to permit persons to whom the Software is furnished to do
-- so, subject to the following conditions:
--
-- The above copyright notice and this permission notice shall be included in all
-- copies or substantial portions of the Software.
--
-- THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
-- IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
-- FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
-- AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
-- LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
-- OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
-- SOFTWARE.
--

-- Locally extended encoder with optional strict, bounded table snapshots.
local json = { _version = "0.1.2" }

-------------------------------------------------------------------------------
-- Encode
-------------------------------------------------------------------------------

local escape_char_map = {
  [ "\\" ] = "\\",
  [ "\"" ] = "\"",
  [ "\b" ] = "b",
  [ "\f" ] = "f",
  [ "\n" ] = "n",
  [ "\r" ] = "r",
  [ "\t" ] = "t",
}

local escape_char_map_inv = { [ "/" ] = "/" }
for k, v in pairs(escape_char_map) do
  escape_char_map_inv[v] = k
end


local function escape_char(c)
  return "\\" .. (escape_char_map[c] or string.format("u%04x", c:byte()))
end


local function validUTF8(s)
  local i = 1
  while i <= #s do
    local b = s:byte(i)
    local count = b < 128 and 1 or b >= 194 and b <= 223 and 2 or b >= 224 and b <= 239 and 3 or b >= 240 and b <= 244 and 4
    if not count or i + count - 1 > #s then return false end
    for j = 1, count - 1 do
      local c = s:byte(i + j)
      if c < 128 or c > 191 then return false end
    end
    local second = s:byte(i + 1)
    if (b == 224 and second < 160) or (b == 237 and second > 159) or
       (b == 240 and second < 144) or (b == 244 and second > 143) then return false end
    i = i + count
  end
  return true
end

-- Optional snapshot policy; defaults retain the protocol's empty arrays and precision.
-- Raw traversal avoids invoking debuggee metamethods in either mode.
function json.encode(value, options)
  options = options or {}
  local active, parts, nodes, bytes = {}, {}, 0, 0
  local function emit(text)
    bytes = bytes + #text
    if options.maxBytes and bytes > options.maxBytes then
      error('JSON exceeds ' .. string.format('%g', options.maxBytes / 1024) .. ' KiB')
    end
    parts[#parts + 1] = text
  end
  local function stringValue(text)
    if options.maxBytes and #text > options.maxBytes then
      error('JSON exceeds ' .. string.format('%g', options.maxBytes / 1024) .. ' KiB')
    end
    if options.validateUTF8 and not validUTF8(text) then error('JSON requires UTF-8 strings; binary strings cannot be exported') end
    emit('"' .. text:gsub('[%z\1-\31\\"]', escape_char) .. '"')
  end
  local visit
  visit = function(v, depth)
    nodes = nodes + 1
    if options.maxValues and nodes > options.maxValues then error('JSON exceeds ' .. options.maxValues .. ' values') end
    if options.maxDepth and depth > options.maxDepth then error('JSON exceeds ' .. options.maxDepth .. ' levels') end
    local kind = type(v)
    if kind == 'nil' then emit('null')
    elseif kind == 'boolean' then emit(v and 'true' or 'false')
    elseif kind == 'string' then stringValue(v)
    elseif kind == 'number' then
      if v ~= v or v == math.huge or v == -math.huge then error('JSON cannot represent NaN or infinity') end
      if options.preciseNumbers and math.type and math.type(v) == 'integer' then emit(tostring(v))
      else emit((string.format(options.preciseNumbers and '%.17g' or '%.14g', v):gsub(',', '.'))) end
    elseif kind == 'table' then
      if active[v] then error('JSON cannot represent circular table references') end
      active[v] = true
      local keys, numeric, maximum = {}, true, 0
      for key in next, v do
        if options.maxValues and #keys >= options.maxValues then error('JSON exceeds ' .. options.maxValues .. ' table entries') end
        keys[#keys + 1] = key
        if type(key) == 'number' and key >= 1 and key % 1 == 0 then maximum = math.max(maximum, key)
        else numeric = false end
      end
      if numeric and maximum == #keys and (#keys > 0 or not options.emptyTableAsObject) then
        emit('[')
        for i = 1, maximum do
          if i > 1 then emit(',') end
          visit(rawget(v, i), depth + 1)
        end
        emit(']')
      else
        for _, key in ipairs(keys) do
          if type(key) ~= 'string' then error('JSON objects require string keys; mixed or sparse numeric keys cannot be exported') end
        end
        if options.sortKeys then table.sort(keys) end
        emit('{')
        for i, key in ipairs(keys) do
          if i > 1 then emit(',') end
          stringValue(key); emit(':'); visit(rawget(v, key), depth + 1)
        end
        emit('}')
      end
      active[v] = nil
    elseif options.unsupportedAsString then
      -- A type marker preserves the field/array slot without invoking __tostring.
      stringValue('<' .. kind .. '>')
    else error('JSON cannot represent Lua ' .. kind .. ' values') end
  end
  visit(value, 0)
  return table.concat(parts)
end

-------------------------------------------------------------------------------
-- Decode
-------------------------------------------------------------------------------

local parse

local function create_set(...)
  local res = {}
  for i = 1, select("#", ...) do
    res[ select(i, ...) ] = true
  end
  return res
end

local space_chars   = create_set(" ", "\t", "\r", "\n")
local delim_chars   = create_set(" ", "\t", "\r", "\n", "]", "}", ",")
local escape_chars  = create_set("\\", "/", '"', "b", "f", "n", "r", "t", "u")
local literals      = create_set("true", "false", "null")

local literal_map = {
  [ "true"  ] = true,
  [ "false" ] = false,
  [ "null"  ] = nil,
}


local function next_char(str, idx, set, negate)
  for i = idx, #str do
    if set[str:sub(i, i)] ~= negate then
      return i
    end
  end
  return #str + 1
end


local function decode_error(str, idx, msg)
  local line_count = 1
  local col_count = 1
  for i = 1, idx - 1 do
    col_count = col_count + 1
    if str:sub(i, i) == "\n" then
      line_count = line_count + 1
      col_count = 1
    end
  end
  error( string.format("%s at line %d col %d", msg, line_count, col_count) )
end


local function codepoint_to_utf8(n)
  -- http://scripts.sil.org/cms/scripts/page.php?site_id=nrsi&id=iws-appendixa
  local f = math.floor
  if n <= 0x7f then
    return string.char(n)
  elseif n <= 0x7ff then
    return string.char(f(n / 64) + 192, n % 64 + 128)
  elseif n <= 0xffff then
    return string.char(f(n / 4096) + 224, f(n % 4096 / 64) + 128, n % 64 + 128)
  elseif n <= 0x10ffff then
    return string.char(f(n / 262144) + 240, f(n % 262144 / 4096) + 128,
                       f(n % 4096 / 64) + 128, n % 64 + 128)
  end
  error( string.format("invalid unicode codepoint '%x'", n) )
end


local function parse_unicode_escape(s)
  local n1 = tonumber( s:sub(1, 4),  16 )
  local n2 = tonumber( s:sub(7, 10), 16 )
   -- Surrogate pair?
  if n2 then
    return codepoint_to_utf8((n1 - 0xd800) * 0x400 + (n2 - 0xdc00) + 0x10000)
  else
    return codepoint_to_utf8(n1)
  end
end


local function parse_string(str, i)
  local res = ""
  local j = i + 1
  local k = j

  while j <= #str do
    local x = str:byte(j)

    if x < 32 then
      decode_error(str, j, "control character in string")

    elseif x == 92 then -- `\`: Escape
      res = res .. str:sub(k, j - 1)
      j = j + 1
      local c = str:sub(j, j)
      if c == "u" then
        local hex = str:match("^[dD][89aAbB]%x%x\\u%x%x%x%x", j + 1)
                 or str:match("^%x%x%x%x", j + 1)
                 or decode_error(str, j - 1, "invalid unicode escape in string")
        res = res .. parse_unicode_escape(hex)
        j = j + #hex
      else
        if not escape_chars[c] then
          decode_error(str, j - 1, "invalid escape char '" .. c .. "' in string")
        end
        res = res .. escape_char_map_inv[c]
      end
      k = j + 1

    elseif x == 34 then -- `"`: End of string
      res = res .. str:sub(k, j - 1)
      return res, j + 1
    end

    j = j + 1
  end

  decode_error(str, i, "expected closing quote for string")
end


local function parse_number(str, i)
  local x = next_char(str, i, delim_chars)
  local s = str:sub(i, x - 1)
  local n = tonumber(s)
  if not n then
    decode_error(str, i, "invalid number '" .. s .. "'")
  end
  return n, x
end


local function parse_literal(str, i)
  local x = next_char(str, i, delim_chars)
  local word = str:sub(i, x - 1)
  if not literals[word] then
    decode_error(str, i, "invalid literal '" .. word .. "'")
  end
  return literal_map[word], x
end


local function parse_array(str, i)
  local res = {}
  local n = 1
  i = i + 1
  while 1 do
    local x
    i = next_char(str, i, space_chars, true)
    -- Empty / end of array?
    if str:sub(i, i) == "]" then
      i = i + 1
      break
    end
    -- Read token
    x, i = parse(str, i)
    res[n] = x
    n = n + 1
    -- Next token
    i = next_char(str, i, space_chars, true)
    local chr = str:sub(i, i)
    i = i + 1
    if chr == "]" then break end
    if chr ~= "," then decode_error(str, i, "expected ']' or ','") end
  end
  return res, i
end


local function parse_object(str, i)
  local res = {}
  i = i + 1
  while 1 do
    local key, val
    i = next_char(str, i, space_chars, true)
    -- Empty / end of object?
    if str:sub(i, i) == "}" then
      i = i + 1
      break
    end
    -- Read key
    if str:sub(i, i) ~= '"' then
      decode_error(str, i, "expected string for key")
    end
    key, i = parse(str, i)
    -- Read ':' delimiter
    i = next_char(str, i, space_chars, true)
    if str:sub(i, i) ~= ":" then
      decode_error(str, i, "expected ':' after key")
    end
    i = next_char(str, i + 1, space_chars, true)
    -- Read value
    val, i = parse(str, i)
    -- Set
    res[key] = val
    -- Next token
    i = next_char(str, i, space_chars, true)
    local chr = str:sub(i, i)
    i = i + 1
    if chr == "}" then break end
    if chr ~= "," then decode_error(str, i, "expected '}' or ','") end
  end
  return res, i
end


local char_func_map = {
  [ '"' ] = parse_string,
  [ "0" ] = parse_number,
  [ "1" ] = parse_number,
  [ "2" ] = parse_number,
  [ "3" ] = parse_number,
  [ "4" ] = parse_number,
  [ "5" ] = parse_number,
  [ "6" ] = parse_number,
  [ "7" ] = parse_number,
  [ "8" ] = parse_number,
  [ "9" ] = parse_number,
  [ "-" ] = parse_number,
  [ "t" ] = parse_literal,
  [ "f" ] = parse_literal,
  [ "n" ] = parse_literal,
  [ "[" ] = parse_array,
  [ "{" ] = parse_object,
}


parse = function(str, idx)
  local chr = str:sub(idx, idx)
  local f = char_func_map[chr]
  if f then
    return f(str, idx)
  end
  decode_error(str, idx, "unexpected character '" .. chr .. "'")
end


function json.decode(str)
  if type(str) ~= "string" then
    error("expected argument of type string, got " .. type(str))
  end
  local res, idx = parse(str, next_char(str, 1, space_chars, true))
  idx = next_char(str, idx, space_chars, true)
  if idx <= #str then
    decode_error(str, idx, "trailing garbage")
  end
  return res
end


return json
