-- Add this directory to package.path, then require('lua-devtools').listen{port=8172}.
local directory = debug.getinfo(1, 'S').source:sub(2):match('^(.*)[/\\]') or '.'
return {
  listen = function(options)
    options = options or {}
    return assert(loadfile(directory .. '/debugger.lua'))(options)
  end,
}
