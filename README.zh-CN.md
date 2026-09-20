# Lua DevTools

[English](README.md) · [简体中文](README.zh-CN.md)

VS Code 的 Lua 调试与语言服务扩展。协议服务端是独立的 Go 二进制，扩展只负责把它们接入 VS Code。

```
VS Code ──DAP──▶ vscode/bin/lua-dap (Go, google/go-dap) ──行协议──▶ vscode/lua/debugger.lua (debug.sethook)
VS Code ──LSP──▶ vscode/bin/lua-lsp (Go, tliron/glsp + tree-sitter-lua)
```

## 功能

- 调试：断点、条件断点、单步（next / step in / step out）、调用栈、局部变量与 upvalue、表格展开、Debug Console 求值（可赋值）、运行时错误处暂停、`stopOnEntry`、不调试直接运行。
- 语言服务：语法诊断、文档大纲、跳转定义、补全、悬停、文件顶部 `Run | Debug` CodeLens。
- 编辑器右上角 Run / Debug 按钮；无 launch.json 时对当前 `.lua` 文件直接 F5。
- 中英文界面。

## 要求

- VS Code ≥ 1.91
- 运行脚本的机器上装有 Lua 5.4（`brew install lua@5.4`、`apt install lua5.4` 等）

## 配置

| 设置 | 说明 |
|---|---|
| `luaDevtools.luaPath` | Lua 解释器路径；为空时依次查找 `lua5.4`、Homebrew `lua@5.4`、`lua` |
| `luaDevtools.trace.server` | `off` / `messages` / `verbose`，在 Output 面板查看 JSON-RPC 报文 |

launch 配置（`type: "lua"`）：

| 字段 | 说明 |
|---|---|
| `program` | 要调试的脚本（必填） |
| `args`、`cwd`、`env` | 参数、工作目录（默认脚本所在目录）、环境变量 |
| `luaPath` | 本配置使用的解释器 |
| `stopOnEntry` | 在第一行暂停 |
| `packagePath` | 追加到 `LUA_PATH` 前面的模块搜索模式，如 `"${workspaceFolder}/src/?.lua"` |
| `packageCPath` | 追加到 `LUA_CPATH` 前面的 C 模块搜索模式 |

## 开发

需要 Node.js 22、Go ≥ 1.22 和 C 编译器（tree-sitter 通过 cgo 编译）、Lua 5.4。

```sh
npm ci
npm run build       # Go 服务端 → vscode/bin/，esbuild 打包 → vscode/dist/
npm test            # go test -race、LSP / DAP stdio 冒烟测试（不需要 VS Code）
npm run test:e2e    # 在真实 VS Code 里跑扩展（首次会下载 VS Code）
npm run package     # 平台专属 VSIX，输出在 vscode/
```

在 VS Code 中打开本目录按 F5：先执行 `npm run build`，再打开扩展开发窗口（工作区为 `examples/`）。

### 目录

| 路径 | 说明 |
|---|---|
| `cmd/lua-dap`、`internal/dap` | DAP 服务端：请求分发、Lua 进程与行协议 |
| `cmd/lua-lsp`、`internal/lsp` | LSP 服务端：glsp handler |
| `internal/analysis` | tree-sitter 解析、作用域 / 符号表、位置转换（含单元测试） |
| `internal/i18n` | Go 服务端的文案（en、zh-cn） |
| `vscode/` | VS Code 扩展：`src/`、`lua/debugger.lua`（运行在被调试进程内）、`l10n/`、`package.nls*.json`、e2e 测试 `src/e2e/` |
| `vscode/lua/json.lua` | vendored [rxi/json.lua](https://github.com/rxi/json.lua)（MIT） |
| `scripts/` | `build-go.mjs`、`package.mjs`、`e2e.mjs`、`*-smoke.mjs` |
| `.github/workflows` | CI 逐平台构建并打包；发布 GitHub Release 时发布到 Marketplace 与 Open VSX |

### 发布

修改 `vscode/package.json` 的 `version`，打 `v<version>` 标签并发布 GitHub Release。`release.yml` 复用 CI 构建，发布 VSIX 并挂到 Release。仓库需配置环境 `marketplace-publish` 及密钥 `AZURE_CLIENT_ID`、`AZURE_TENANT_ID`（Marketplace 工作负载身份联合）和 `OVSX_PAT`。

## 已知限制

- 纯 Lua 无法非阻塞读 stdin：运行中修改的断点在下一次暂停后才生效；不支持 `pause` 请求。
- `print` / `io.write` 被接管走协议通道；脚本直接 `io.stdout:write` 会被当作普通输出行显示。
- 不支持协程内断点、attach、嵌入式 Lua。
- 语法错误只有位置和粗略描述（tree-sitter 的 ERROR / MISSING 节点）。
- `.` / `:` 之后不提供补全（没有类型信息）。
- 没有 win32-arm64 包（没有 GitHub 托管 runner，tree-sitter 需要 cgo）。

## 计划

- `.` / `:` 之后的字段补全、未定义全局变量警告、跨文件 `require` 解析。
- win32-arm64 包。
