# Lua DevTools

[English](README.md) · [简体中文](README.zh-CN.md)

Lua debugging and language support for VS Code. The protocol servers are standalone Go binaries; the extension only wires them into VS Code.

```
VS Code ──DAP──▶ vscode/bin/lua-dap (Go, google/go-dap) ──line protocol──▶ vscode/lua/debugger.lua (debug.sethook)
VS Code ──LSP──▶ vscode/bin/lua-lsp (Go, tliron/glsp + tree-sitter-lua)
```

## Features

- Debugging: breakpoints, conditional breakpoints, stepping (next / step in / step out), call stack, locals and upvalues, table expansion, Debug Console evaluation (assignments work), pause at runtime errors, `stopOnEntry`, run without debugging.
- Language server: syntax diagnostics, outline, go to definition, completion, hover, `Run | Debug` code lenses at the top of each file.
- Run / Debug buttons in the editor title; F5 on the active `.lua` file works without a launch.json.
- English and Simplified Chinese UI.

## Requirements

- VS Code ≥ 1.91
- Lua 5.4 on the machine that runs the scripts (`brew install lua@5.4`, `apt install lua5.4`, …)

## Settings

| Setting | Description |
|---|---|
| `luaDevtools.luaPath` | Lua interpreter; when empty, `lua5.4`, Homebrew `lua@5.4` and `lua` are tried in order |
| `luaDevtools.trace.server` | `off` / `messages` / `verbose` JSON-RPC trace in the Output panel |

Launch configuration (`type: "lua"`):

| Field | Description |
|---|---|
| `program` | Script to debug (required) |
| `args`, `cwd`, `env` | Arguments, working directory (defaults to the script's directory) and environment variables |
| `luaPath` | Interpreter for this configuration |
| `stopOnEntry` | Pause on the first line |
| `packagePath` | Module search patterns prepended to `LUA_PATH`, e.g. `"${workspaceFolder}/src/?.lua"` |
| `packageCPath` | C module search patterns prepended to `LUA_CPATH` |

## Development

Requirements: Node.js 22, Go ≥ 1.22 with a C compiler (tree-sitter is built through cgo), Lua 5.4.

```sh
npm ci
npm run build       # Go servers into vscode/bin/ + esbuild bundle into vscode/dist/
npm test            # go test -race, LSP and DAP smoke tests over stdio (no VS Code needed)
npm run test:e2e    # the extension inside a real VS Code (downloads VS Code on first run)
npm run package     # platform-specific VSIX in vscode/
```

Open this folder in VS Code and press F5: `npm run build` runs, then an Extension Development Host opens on `examples/`.

### Layout

| Path | Description |
|---|---|
| `cmd/lua-dap`, `internal/dap` | DAP server: request dispatch, Lua process and line protocol |
| `cmd/lua-lsp`, `internal/lsp` | LSP server: glsp handlers |
| `internal/analysis` | tree-sitter parsing, scopes / symbols, position conversion (unit tested) |
| `internal/i18n` | Messages of the Go servers (en, zh-cn) |
| `vscode/` | The VS Code extension: `src/`, `lua/debugger.lua` (runs inside the debuggee), `l10n/`, `package.nls*.json`, e2e tests in `src/e2e/` |
| `vscode/lua/json.lua` | vendored [rxi/json.lua](https://github.com/rxi/json.lua) (MIT) |
| `scripts/` | `build-go.mjs`, `package.mjs`, `e2e.mjs`, `*-smoke.mjs` |
| `.github/workflows` | CI builds and packages every platform; a published GitHub Release publishes to the Marketplace and Open VSX |

### Release

Bump `version` in `vscode/package.json`, tag `v<version>` and publish a GitHub Release. `release.yml` reuses the CI build, publishes the VSIX packages and attaches them to the release. Required repository configuration: environment `marketplace-publish` with secrets `AZURE_CLIENT_ID`, `AZURE_TENANT_ID` (Marketplace workload identity federation) and `OVSX_PAT`.

## Known limitations

- Pure Lua cannot read stdin without blocking: breakpoints changed while running apply at the next pause; the `pause` request is unsupported.
- `print` / `io.write` are routed through the protocol channel; a script writing `io.stdout:write` directly is shown as plain output lines.
- No breakpoints inside coroutines, no attach, no embedded Lua.
- Syntax errors carry a position and a rough description only (tree-sitter ERROR / MISSING nodes).
- No completion after `.` / `:` (no type information).
- No win32-arm64 package (no GitHub-hosted runner; tree-sitter needs cgo).

## Planned

- Field completion after `.` / `:`, undefined-global warnings, cross-file `require` resolution.
- win32-arm64 package.
