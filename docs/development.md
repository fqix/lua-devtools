# Development

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

See the [example guide](../examples/README.md) for blocking sleep, cooperative coroutine scheduling, coroutine errors, and live pause/breakpoint exercises.

The CodeLens end-to-end tests require `luaunit` 3.4 and `busted` 2.2.0 installed for Lua 5.4. If using a custom LuaRocks tree, export its `LUA_PATH` and `LUA_CPATH` before running `npm run test:e2e`; CI installs these dependencies in a temporary tree.

[docs/architecture.md](architecture.md) explains how the extension, `lua-lsp`, `lua-dap` and `debugger.lua` fit together, with sequence and flow diagrams for each LSP and DAP feature.

### Layout

| Path | Description |
|---|---|
| `cmd/lua-dap`, `internal/dap` | DAP server: request dispatch, Lua process and line protocol |
| `cmd/lua-lsp`, `internal/lsp` | LSP server: glsp handlers |
| `internal/analysis` | tree-sitter parsing, scopes / symbols, position conversion (unit tested) |
| `internal/i18n` | Messages of the Go servers (en, zh-cn) |
| `vscode/` | The VS Code extension: `src/`, `lua/debugger.lua` (runs inside the debuggee), `l10n/`, `package.nls*.json`, e2e tests in `src/e2e/` |
| `vscode/lua/json.lua` | vendored [rxi/json.lua](https://github.com/rxi/json.lua) (MIT), with local encoder options |
| `scripts/` | `build-go.mjs`, `package.mjs`, `e2e.mjs`, `*-smoke.mjs` |
| `.github/workflows` | CI builds and packages every platform; a published GitHub Release publishes to the Marketplace and Open VSX |

### Release

CI packages darwin-x64/arm64, linux-x64/arm64, and win32-x64/arm64. Windows ARM64 uses LLVM-MinGW for cgo; its Go tests run without the unsupported race detector.

Update `vscode/CHANGELOG.md` and the version in `vscode/package.json` and `package-lock.json`, tag `v<version>` and publish a GitHub Release. `release.yml` reuses the CI build, publishes the VSIX packages and attaches them to the release. Required repository configuration: environment `marketplace-publish` with secrets `AZURE_CLIENT_ID`, `AZURE_TENANT_ID` (Marketplace workload identity federation) and `OVSX_PAT`.

To retry publishing, open Actions → Release → Run workflow and enter an existing tag (for example, `v0.1.0`). The retry uses the six platform VSIX assets from that release and skips versions already published to each store.

### Extended regression tests

VS Code E2E needs LuaSocket, LuaUnit 3.4 and Busted 2.2.0 for Lua 5.4. Export `STYLUA_TEST_BINARY`, `LUAROCKS_TEST_BINARY` and `LUAROCKS_TEST_LUA` to exercise real formatting and the offline package install/upgrade/dependency-protected uninstall lifecycle. Export the rock tree’s `LUA_PATH` and `LUA_CPATH`; set `LUA_TEST_SOCKET_REQUIRED=1` to fail instead of skipping TCP tests. CI enables these on its E2E runners. `npm run test:extension` also runs pure TypeScript tests on every platform.
