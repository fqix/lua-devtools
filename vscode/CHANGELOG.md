# Changelog

## Unreleased

- Use the project logo for the Lua Environments sidebar and interpreter status bar.
- Show standard-library functions and third-party Lua/native modules beneath each interpreter, including project dependencies and interpreter search paths.
- Add Lua document and format-on-save support through StyLua, with project configuration, ignore rules and a configurable executable path.

## 0.3.0

### Added

- Lua Environments sidebar: discover interpreters, inspect paths, switch the active interpreter, refresh discovery, enter a custom path and restore automatic selection.
- Install LuaRocks packages from the interpreter's package icon. Dependencies are isolated by project and interpreter under `.lua-devtools/rocks/` and loaded automatically by new run/debug sessions.
- Configure the LuaRocks executable with `luaDevtools.luarocksPath`; installation progress and errors appear in the task terminal.

### Changed

- Simplify the English and Chinese READMEs and add separate architecture and development guides.

## 0.2.0

### Added

- Discover and select Lua interpreters from the status bar; use the selected interpreter for running, debugging, syntax checks and standard-library completion.
- Lua 5.1 and LuaJIT 2.1 support alongside Lua 5.2–5.5.
- Lua 5.5 declaration navigation and outline support, including lexical `global` declarations, attributes and named varargs.
- Go to implementations of statically resolved local module functions, including aliases, methods, directory modules and unsaved sources.
- Read-only JSON views for paused table variables. Nested data retains numeric precision; function, userdata and thread values use type markers. Snapshots enforce depth, size and value limits without invoking table metamethods.
- Debugging examples for coroutines, blocking sleep, local and installed modules, and live runtime control.

### Changed

- A version-neutral native polling helper enables pause requests and breakpoint updates while Lua is running, with a pure Lua fallback when unavailable. Blocking C calls still wait until Lua execution resumes.
- Build tooling uses Node.js 24 LTS.

### Fixed

- Handle Windows Lua text-output line endings.
- Require all runtime artifacts before packaging platform VSIX files.
- Allow store publication retries using existing release assets.

## 0.1.0

- Lua debugger: breakpoints, conditional breakpoints, stepping, call stack, locals/upvalues, table expansion, evaluate, pause on runtime errors (`bin/lua-dap` + `lua/debugger.lua`).
- Lua language server: diagnostics, outline, go to definition, completion, hover, Run/Debug code lenses (`bin/lua-lsp`, tree-sitter based).
- Launch options `packagePath`, `packageCPath`, `env`; English and Simplified Chinese UI.
- Platform-specific VSIX packages built by GitHub Actions.
