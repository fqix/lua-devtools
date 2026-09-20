# Lua DevTools

[English](README.md) · [简体中文](README.zh-CN.md)

Lua debugging and language support for VS Code. The protocol servers are standalone Go binaries; the extension only wires them into VS Code.

```
VS Code ──DAP──▶ vscode/bin/lua-dap (Go, google/go-dap) ──line protocol──▶ vscode/lua/debugger.lua (debug.sethook)
VS Code ──LSP──▶ vscode/bin/lua-lsp (Go, tliron/glsp + tree-sitter-lua)
```

## Features

- Debugging: breakpoints, conditional breakpoints, stepping (next / step in / step out), call stack, locals and upvalues, table expansion, Debug Console evaluation (assignments work), pause at runtime errors, `stopOnEntry`, run without debugging.
- Language server: syntax diagnostics with missing-delimiter/block context, outline, go to definition, completion (including standard-library and statically known table members), hover, `Run | Debug` code lenses at the top of each file.
- Run / Debug buttons in the editor title; F5 on the active `.lua` file works without a launch.json.
- Coroutine breakpoints and stepping for `coroutine.create` / `coroutine.wrap`, with a coroutine list, suspended stacks, locals/upvalues, and evaluation or assignment in the selected frame.
- Member completion through table aliases, table-valued `__index`, simple functions returning table literals, and top-level exports from local `require` modules.
- F12 / Ctrl/Cmd-click follows statically resolved local `require` exports to function implementations, including aliases, methods, directory modules and unsaved sources.
- Lua 5.1–5.5 and LuaJIT 2.1 debugging; in trusted workspaces, syntax checking and standard-library completion follow Lua 5.1–5.5 and LuaJIT 2.1 interpreter versions.
- Lua 5.5 declarations: definition navigation and outline for `global` / `global function`, lexical shadowing, prefixed attributes, and named vararg parameters (`...args`). `global *` and `global <const> *` preserve implicit global lookup without creating fake outline symbols.
- Program stdout/stderr are separate from the debugger protocol, including direct `io.stdout:write` and output without newlines.
- English and Simplified Chinese UI.

## Native polling helper

No Lua interpreter is bundled. Each OS/architecture package includes one optional C module using only four stable Lua C API functions, without linking a specific liblua version. macOS/Linux resolve symbols from the host process; Windows locates exports in loaded modules regardless of DLL name. The interpreter must support dynamic loading and export the required symbols; Windows also falls back if multiple Lua API providers are found. A missing or unloadable module automatically falls back to pure Lua debugging.

The module checks stdin readiness so the debugger can receive pause requests and breakpoint updates while running. Interpreter-aware diagnostics and standard-library completion support Lua 5.1–5.5 and LuaJIT 2.1, including Lua 5.1 environment functions and LuaJIT’s `bit` / `jit` libraries.

## Test CodeLens

LuaUnit tests importing `luaunit` get **Run Test / Debug Test** above global `test*` / `Test*` functions and methods such as `function TestFoo:testBar()`. The test file must call the LuaUnit runner and accept its normal command-line arguments (for example, `os.exit(lu.LuaUnit.run())`). Each action passes the exact test name, retaining setup/teardown behavior.

Busted tests get the same actions above `it`, `spec` and `test` inside `describe`, `context`, `insulate` and `expose` blocks. Top-level cases are supported too. Selection uses an escaped, anchored full-name filter; the runner executes inside the selected Lua interpreter so breakpoints stay in the original spec file. The working directory is the workspace folder (or the spec directory outside a workspace), including its `.busted` configuration; its `lua` option is ignored so the selected interpreter and debugger remain in use.

Install the chosen framework for the interpreter selected by `luaDevtools.luaPath`; frameworks are not bundled with the extension. See [LuaUnit selection](https://luaunit.readthedocs.io/en/latest/3_getting-started.html#using-the-command-line) and [Busted installation](https://lunarmodules.github.io/busted/#usage). Examples: `examples/test_luaunit.lua` and `examples/example_spec.lua`.

Discovery is static: local LuaUnit suites, dynamically generated cases/names, custom DSL aliases and duplicate Busted full names are not offered. File-level Run / Debug remains available; a Busted spec without its own runner should use the per-test actions.

## Requirements

- VS Code ≥ 1.91
- Lua 5.1–5.5 or LuaJIT 2.1 on the machine that runs the scripts (`brew install lua@5.4`, `apt install lua5.4`, …)

## Settings

With a Lua file open, click the interpreter version in the status bar or run **Lua DevTools: Select Interpreter**. The picker discovers Lua 5.1–5.5 / LuaJIT 2.1 on PATH and in common Homebrew locations. You can also enter an absolute executable path or a command on PATH, or restore automatic detection. Selection updates workspace `luaDevtools.luaPath` (user settings when no workspace is open) and restarts the language server; subsequent runs and debug sessions use it. Multi-root workspaces share this selection; a launch configuration’s `luaPath` still takes precedence. Interpreter probing requires workspace trust.

| Setting | Description |
|---|---|
| `luaDevtools.luaPath` | Lua interpreter; when empty, `lua5.4`, Homebrew `lua@5.4` and `lua` are tried in order |
| `luaDevtools.trace.server` | `off` / `messages` / `verbose` JSON-RPC trace in the Output panel |

Set `luaDevtools.luaPath` to select the interpreter for both language support and debugging; a launch configuration's `luaPath` overrides only that debug session. Automatic discovery still prefers Lua 5.4; executables named only `lua5.2`, `lua5.3`, or `lua5.5` must be selected explicitly. Changing the setting restarts the language server.

In trusted workspaces, syntax checking and standard-library completion follow the selected Lua 5.1–5.5 or LuaJIT 2.1 interpreter. Syntax checking compiles the document without executing it and disables `LUA_INIT`. Without a working interpreter, or in untrusted workspaces, analysis falls back to the built-in Lua 5.4 support.

Static `require("foo.bar")` completion searches `foo/bar.lua` and `foo/bar/init.lua` under the containing workspace root, or the document directory for files outside a workspace. Unsaved open documents take precedence over disk files; modules are never executed.

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

Requirements: Node.js 24 LTS, the Go version in `go.mod` with a C compiler (tree-sitter is built through cgo), and Lua 5.1–5.5 or LuaJIT 2.1.

```sh
npm ci
npm run build       # Go servers into vscode/bin/ + esbuild bundle into vscode/dist/
npm test            # Go unit/integration tests (-race where supported), LSP/DAP smoke tests
npm run test:e2e    # the extension inside a real VS Code (downloads VS Code on first run)
npm run package     # platform-specific VSIX in vscode/
```

CI runs a separate Linux compatibility matrix against Lua **5.1.5, 5.2.4, 5.3.6, 5.4.9, and 5.5.1**, built from checksum-verified official sources. Each version runs the Go DAP and LSP integration tests with the race detector and the DAP smoke test. Native polling and pure Lua fallback are both tested. A separate job tests DAP and LSP against a pinned LuaJIT 2.1 commit. The selected interpreter's version is checked explicitly; a missing or mismatched interpreter fails the job.

The platform jobs build and package all six targets. DAP tests run on Linux, macOS and Windows; Windows builds Lua with a custom DLL name to verify dynamic API resolution. VS Code end-to-end tests run on Linux x64 and macOS ARM64.

To reproduce a matrix entry locally:

```sh
LUA_TEST_BINARY=/absolute/path/to/lua LUA_TEST_VERSION=5.2.4 npm run test:go
LUA_TEST_BINARY=/absolute/path/to/lua npm run test:dap
```

Replace the executable path and expected version together. These variables select Lua for Go integration tests and DAP smoke tests; VS Code end-to-end tests use the extension's interpreter settings and discovery.

Open this folder in VS Code and press F5: `npm run build` runs, then an Extension Development Host opens on `examples/`.

See the [example guide](examples/README.md) for blocking sleep, cooperative coroutine scheduling, coroutine errors, and live pause/breakpoint exercises.

The CodeLens end-to-end tests require `luaunit` 3.4 and `busted` 2.2.0 installed for Lua 5.4. If using a custom LuaRocks tree, export its `LUA_PATH` and `LUA_CPATH` before running `npm run test:e2e`; CI installs these dependencies in a temporary tree.

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

CI packages darwin-x64/arm64, linux-x64/arm64, and win32-x64/arm64. Windows ARM64 uses LLVM-MinGW for cgo; its Go tests run without the unsupported race detector.

Bump `version` in `vscode/package.json`, tag `v<version>` and publish a GitHub Release. `release.yml` reuses the CI build, publishes the VSIX packages and attaches them to the release. Required repository configuration: environment `marketplace-publish` with secrets `AZURE_CLIENT_ID`, `AZURE_TENANT_ID` (Marketplace workload identity federation) and `OVSX_PAT`.

To retry publishing, open Actions → Release → Run workflow and enter an existing tag (for example, `v0.1.0`). The retry uses the six platform VSIX assets from that release and skips versions already published to each store.

## Known limitations

- **Runtime control:** pause and breakpoint updates are processed at the next Lua debug hook; blocking C functions and system calls cannot be interrupted. Without the native helper, pause is unavailable and breakpoint changes wait until the next stop. Interactive program stdin is unavailable.
- **Coroutine execution control:** stepping a coroutine other than the currently stopped one and independently resuming other suspended coroutines are unsupported. Errors caught by `coroutine.resume` do not pause inside the failed coroutine. Replacing the debugger's hooks is unsupported. Lua 5.1 / LuaJIT cannot inspect the suspended main thread from a stopped coroutine; JIT compilation is disabled while debugging LuaJIT.
- **Integration:** no attach to an existing process or embedded Lua support.
- **Complex type inference:** no control-flow merging, function-valued `__index`, or complex return-value inference.
- **Module resolution:** nested exports, custom `package.path`, C modules and dynamic loaders are unsupported; file-size and dependency-depth limits apply.
- **Diagnostics:** interpreter errors are in English, report the first syntax error with a line-level range, and have no automatic fixes. Some fallback diagnostics remain generic.

## Planned

- Broader control-flow and return-value inference, undefined-global warnings, custom module search paths and nested module exports.
- Attach and embedded Lua integration.
