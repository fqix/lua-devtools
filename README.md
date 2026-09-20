# Lua DevTools

[English](README.md) · [简体中文](README.zh-CN.md)

Lua debugging and language support for VS Code, supporting **Lua 5.1–5.5 and LuaJIT 2.1** on macOS, Linux and Windows.

## Quick start

1. Install Lua DevTools from the [VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=fqix.lua-devtools), or install a VSIX from [Releases](https://github.com/fqix/lua-devtools/releases). Requires VS Code 1.91 or later.
2. Install a Lua interpreter separately, then select it from the status bar or **Lua DevTools → Lua Environments**. Interpreter discovery requires a trusted workspace.
3. Open a `.lua` file and press **F5** to debug, or use **Run** in the editor title. No `launch.json` is required.

## Features

- Breakpoints, conditional breakpoints, stepping, call stacks, coroutine inspection and Debug Console evaluation.
- Syntax diagnostics, completion, hover, outline and definition navigation, including Lua 5.5 declarations and statically resolved local modules.
- Interpreter discovery, switching and project-local package installation.
- Read-only table JSON views and per-test Run/Debug actions for LuaUnit and Busted.
- English and Simplified Chinese UI.

## Environments and packages

In **Lua Environments**, click an interpreter to select it, expand it to see its path, use **+** to enter a path, or refresh to rescan. Selection applies to subsequent runs and debug sessions; a launch configuration’s `luaPath` takes precedence.

The selected environment expands to show standard libraries and their functions, plus third-party modules from project packages and interpreter search paths. Lua sources open on click; native modules reveal their file location. Refresh after external installations. Scanning does not execute third-party modules or account for runtime changes to `package.path`.

Click the **package icon** beside an interpreter to install a package such as `luasocket`. Requires LuaRocks; set `luaDevtools.luarocksPath` if it is not on PATH. Installation logs appear in the task terminal.

Dependencies are isolated by project and interpreter under `.lua-devtools/rocks/` and loaded automatically by new Run/Debug sessions. Add `.lua-devtools/` to your `.gitignore`. Native packages may require a compiler and Lua development headers. External terminals are not configured automatically.

## Tables and tests

While paused, right-click a table in Variables and choose **View Table as JSON**. Snapshots retain numeric precision and use type markers for functions, userdata and threads. Limits: 32 levels, 10,000 values and 512 KiB. Circular references, mixed/sparse keys, binary strings and non-finite numbers are unsupported.

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
| `luaDevtools.trace.server` | Language-server logging: `off`, `messages` or `verbose` |

For a custom `launch.json`, use `type: "lua"`, `request: "launch"` and `program`. Optional fields: `args`, `cwd`, `env`, `luaPath`, `stopOnEntry`, `packagePath` and `packageCPath`.

## Limitations

- No attach, embedded Lua debugging or interactive program stdin.
- Pause and live breakpoint updates require the bundled native helper. Blocking C calls must return to Lua first.
- Independently resuming suspended coroutines is unsupported; errors caught by `coroutine.resume` do not stop inside the failed coroutine. LuaJIT compilation is disabled while debugging.
- Language analysis is static; dynamic loaders, custom module search paths and installed package-source navigation are unsupported. Without a trusted, working interpreter, analysis falls back to Lua 5.4.

## Documentation

- [Examples](examples/README.md)
- [Development, testing and releases](docs/development.md)
- [Architecture](docs/architecture.md)
- [Changelog](vscode/CHANGELOG.md)
