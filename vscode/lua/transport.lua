-- Optional TCP control channel. LuaSocket is required only for attach/interactive mode.
local options = ...
local socket = require('socket')
local connection
if options.connect then
  local host, port = options.connect:match('^(.+):(%d+)$')
  connection = assert(socket.tcp())
  connection:settimeout(5)
  assert(connection:connect(host, tonumber(port)))
  assert(connection:send(options.encode({token=options.token or ''}) .. '\n'))
else
  local host = options.host or '127.0.0.1'
  assert(host == '127.0.0.1' or host == '::1' or host == 'localhost' or (options.token and #options.token > 0), 'non-loopback listeners require a token')
  local listener = assert(socket.bind(host, options.port or 8172))
  if options.ready then options.ready(listener:getsockname()) end
  listener:settimeout(options.timeout or 120)
  local err
  connection, err = listener:accept()
  listener:close()
  assert(connection, err)
  connection:settimeout(5)
  local line, readErr = connection:receive('*l')
  local ok, auth = pcall(options.decode, line or '')
  if not ok or type(auth) ~= 'table' or auth.token ~= (options.token or '') then
    connection:close()
    error(readErr or 'invalid debugger token')
  end
end
connection:settimeout(nil)
local buffered, partial, closed
return {
  send = function(text)
    if closed then return nil end
    connection:settimeout(5)
    local ok = connection:send(text)
    if not ok then closed = true end
    return ok
  end,
  poll = function()
    if buffered or closed then return true end
    connection:settimeout(0)
    local line, err, prefix = connection:receive('*l', partial)
    partial = prefix
    if line then buffered, partial = line, nil end
    if err == 'closed' then closed = true end
    return buffered ~= nil or closed
  end,
  read = function()
    if buffered then local line = buffered; buffered = nil; return line end
    if closed then return nil end
    connection:settimeout(nil)
    local line = connection:receive('*l', partial)
    partial = nil
    if not line then closed = true end
    return line
  end,
  close = function() closed = true; connection:close() end,
}
