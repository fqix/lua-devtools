# Lua DevTools

[![VS Code Marketplace installs](https://vsmarketplacebadges.dev/installs-short/fqix.lua-devtools.svg?label=VS%20Code%20Marketplace%20installs)](https://marketplace.visualstudio.com/items?itemName=fqix.lua-devtools)
[![Open VSX downloads](https://img.shields.io/open-vsx/dt/fqix/lua-devtools?label=Open%20VSX%20downloads)](https://open-vsx.org/extension/fqix/lua-devtools)

[English](README.md) · [简体中文](README.zh-CN.md)

Lua debugging and language support for VS Code, supporting **Lua 5.1–5.5 and LuaJIT 2.1** on macOS, Linux and Windows.

## Quick start

1. Install Lua DevTools from the [VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=fqix.lua-devtools), or install a VSIX from [Releases](https://github.com/fqix/lua-devtools/releases). Requires VS Code 1.91 or later.
2. Install a Lua interpreter separately, then select it from the status bar or **Lua DevTools → Lua Environments**. Interpreter discovery requires a trusted workspace.
3. Open a `.lua` file and press **F5** to debug, or use **Run** in the editor title. No `launch.json` is required.

## Features

- Breakpoints, conditional breakpoints, stepping, call stacks, coroutine inspection and Debug Console evaluation.
- Syntax diagnostics, completion, hover, outline, definitions, signature help, and workspace-wide references and rename for locals, globals and statically resolved table members, including Lua 5.5 declarations and local modules.
- Interpreter discovery, switching and project-local package installation.
- Read-only table JSON views and per-test Run/Debug actions for LuaUnit and Busted.
- English and Simplified Chinese UI.

## Environments and packages

In **Lua Environments**, click an interpreter to select it, expand it to see its path, use **+** to enter a path, or refresh to rescan. Selection applies to subsequent runs and debug sessions; a launch configuration’s `luaPath` takes precedence.

The selected environment expands to show standard libraries and their functions, plus third-party modules from project packages and interpreter search paths. Lua sources open on click; native modules reveal their file location. F12 and module-member completion use the selected interpreter’s Lua search paths, project packages and the `packagePath` entries of Lua configurations in `launch.json` (with `${workspaceFolder}`, `${env:NAME}` and similar folder-level variables; relative entries follow the configuration's `cwd`, or the workspace folder when there is none). Refresh after external installations. Scanning does not execute third-party modules or account for runtime changes to `package.path`.

Click the **package icon** beside an interpreter, or the install button on **Project packages**, to install `luasocket` or a specific version such as `luaunit@3.5-1`. **Project packages** lists installed versions; use the upgrade button or right-click to install another version or uninstall that version. LuaRocks blocks removal when it would break dependencies. Requires LuaRocks; set `luaDevtools.luarocksPath` if it is not on PATH. Installation logs appear in the task terminal.

Dependencies are isolated by project and interpreter under `.lua-devtools/rocks/` and loaded automatically by new Run/Debug sessions. Add `.lua-devtools/` to your `.gitignore`. Native packages may require a compiler and Lua development headers. External terminals are not configured automatically.

## Tables and tests

While paused, right-click a table in Variables and choose **View Table as JSON**. Snapshots retain numeric precision and use type markers for functions, userdata and threads. Cyclic references, mixed/sparse or object keys, binary strings and non-finite numbers use a tagged structure with `$format: "lua-table-v1"`. Limits: 32 levels, 10,000 values and 512 KiB.

Install `luaunit` or `busted` for the selected interpreter to use **Run Test / Debug Test** above statically recognized cases. LuaUnit files must call their runner; Busted cases use the extension’s runner. See the [examples](examples/README.md).

## Formatting

Install [StyLua](https://github.com/JohnnyMorganz/StyLua), or set `luaDevtools.styluaPath`, then choose **Format Document** in a trusted workspace. Project `stylua.toml` / `.stylua.toml` and `.styluaignore` files are respected. Syntax support depends on the installed StyLua version; errors leave the document unchanged.

To format on save:

```json
"[lua]": {
  "editor.defaultFormatter": "fqix.lua-devtools",
  "editor.formatOnSave": true
}
```

## Configuration

| Setting | Purpose |
|---|---|
| `luaDevtools.luaPath` | Interpreter path; automatic lookup prefers `lua5.4`, Homebrew `lua@5.4`, then `lua` |
| `luaDevtools.luarocksPath` | LuaRocks executable; defaults to `luarocks` on PATH |
| `luaDevtools.probeNativeModules` | Off by default. Load C modules (`.so` / `.dll` from project packages, the interpreter's `package.cpath` and launch `packageCPath`) in the selected interpreter to complete their members after `require`. This executes third-party native code; enable it only for packages you trust |
| `luaDevtools.trace.server` | Language-server logging: `off`, `messages` or `verbose` |

For a custom `launch.json`, use `type: "lua"`, `request: "launch"` and `program`. Optional fields: `args`, `cwd`, `env`, `luaPath`, `stopOnEntry`, `packagePath` and `packageCPath`.

## More debugging modes

TCP attach supports Lua and embedded hosts that load the debugger; disconnect leaves the host running. Set `interactive: true` on a launch configuration to send stdin or EOF from the Command Palette. Both modes require LuaSocket. While paused, you can resume a suspended coroutine independently; `breakOnCoroutineErrors: true` enables inspection of errors caught by `coroutine.resume`. See the [debugging guide](docs/debugging.md).

## Limitations

- Pause and live breakpoint updates use the bundled native helper in ordinary launch sessions, or LuaSocket polling in TCP mode. Blocking C calls must return to Lua; F11 cannot enter C implementations such as `os.time` or `cjson.encode`.
- LuaJIT compilation is disabled while debugging. Attach requires cooperative host setup; it cannot inject into an arbitrary process.
- References and rename follow lexical bindings, globals and table members that static analysis can attribute to one table (module exports, `__index` chains, aliases). Dynamic keys (`t[name]`, `_G[name]`), fields of untyped values such as parameters or `self`, and instances returned by constructor functions are not followed. Renames are rejected when they would capture or merge bindings, when nested tables are replaced or reassignment makes table bindings (including aliases) uncertain, or when the member is declared in an installed package or outside the workspace. Signature help uses static function declarations.
- Language analysis is static: dynamic loaders, runtime changes to `package.path` and editor-dependent launch variables such as `${file}` are not resolved. Members of C modules are only known with `luaDevtools.probeNativeModules`, and then without definitions or signatures. Without a trusted, working interpreter, analysis falls back to Lua 5.4.

## Documentation

- [Examples](examples/README.md)
- [Development, testing and releases](docs/development.md)
- [Architecture](docs/architecture.md)
- [Changelog](vscode/CHANGELOG.md)
