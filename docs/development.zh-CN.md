# 开发指南

需要 Node.js 24 LTS、`go.mod` 指定版本的 Go 和 C 编译器（tree-sitter 通过 cgo 编译）、Lua 5.1–5.5 或 LuaJIT 2.1。

```sh
npm ci
npm run build       # Go 服务端 → vscode/bin/，esbuild 打包 → vscode/dist/
npm test            # Go 单元/集成测试（支持的平台启用 -race）、LSP / DAP 冒烟测试
npm run test:e2e    # 在真实 VS Code 里跑扩展（首次会下载 VS Code）
npm run package     # 平台专属 VSIX，输出在 vscode/
```

CI 单独运行 Linux 兼容性矩阵，覆盖 **Lua 5.1.5、5.2.4、5.3.6、5.4.9、5.5.1**。各版本从校验 SHA-256 的官方源码构建，运行启用 race detector 的 Go DAP、LSP 集成测试和 DAP 冒烟测试。各版本验证原生轮询及纯 Lua 回退，LuaJIT 2.1 使用单独的固定提交测试任务验证 DAP 和 LSP。测试会显式核对所选解释器的版本；解释器不存在或版本不匹配会直接失败。

平台任务构建并打包全部六种目标。Linux、macOS 和 Windows 均运行 DAP 测试；Windows 构建自定义 DLL 名称的 Lua，验证动态 API 解析。Linux x64 和 macOS ARM64 运行真实 VS Code 端到端测试。

本地复现指定版本：

```sh
LUA_TEST_BINARY=/absolute/path/to/lua LUA_TEST_VERSION=5.2.4 npm run test:go
LUA_TEST_BINARY=/absolute/path/to/lua npm run test:dap
```

请同时替换解释器路径和预期版本。这两个环境变量用于 Go 集成测试和 DAP 冒烟测试；真实 VS Code 端到端测试使用扩展的解释器设置及自动查找逻辑。

在 VS Code 中打开本目录按 F5：先执行 `npm run build`，再打开扩展开发窗口（工作区为 `examples/`）。

参见[示例指南](../examples/README.md)，体验阻塞 sleep、协程定时调度、协程错误，以及运行中暂停和修改断点。

CodeLens 端到端测试需要为 Lua 5.4 安装 `luaunit` 3.4 和 `busted` 2.2.0。使用自定义 LuaRocks 目录时，先导出对应的 `LUA_PATH` 和 `LUA_CPATH`，再运行 `npm run test:e2e`；CI 会将这些依赖安装到临时目录。

[docs/architecture.zh-CN.md](architecture.zh-CN.md) 说明扩展、`lua-lsp`、`lua-dap` 和 `debugger.lua` 如何协作，并为每个 LSP / DAP 功能提供时序图和流程图。

### 目录

| 路径 | 说明 |
|---|---|
| `cmd/lua-dap`、`internal/dap` | DAP 服务端：请求分发、Lua 进程与行协议 |
| `cmd/lua-lsp`、`internal/lsp` | LSP 服务端：glsp handler |
| `internal/analysis` | tree-sitter 解析、作用域 / 符号表、位置转换（含单元测试） |
| `internal/i18n` | Go 服务端的文案（en、zh-cn） |
| `vscode/` | VS Code 扩展：`src/`、`lua/debugger.lua`（运行在被调试进程内）、`l10n/`、`package.nls*.json`、e2e 测试 `src/e2e/` |
| `vscode/lua/json.lua` | vendored [rxi/json.lua](https://github.com/rxi/json.lua)（MIT），编码器扩展了快照选项 |
| `scripts/` | `build-go.mjs`、`package.mjs`、`e2e.mjs`、`*-smoke.mjs` |
| `.github/workflows` | CI 逐平台构建并打包；发布 GitHub Release 时发布到 Marketplace 与 Open VSX |

### 发布

CI 打包 darwin-x64/arm64、linux-x64/arm64 和 win32-x64/arm64。Windows ARM64 使用 LLVM-MinGW 编译 cgo；该平台不支持 Go race detector，因此运行普通测试。

更新 `vscode/CHANGELOG.md`、`vscode/package.json` 和 `package-lock.json` 中的版本号，打 `v<version>` 标签并发布 GitHub Release。`release.yml` 复用 CI 构建，发布 VSIX 并挂到 Release。仓库需配置环境 `marketplace-publish` 及密钥 `AZURE_CLIENT_ID`、`AZURE_TENANT_ID`（Marketplace 工作负载身份联合）和 `OVSX_PAT`。

发布失败后，可在 Actions → Release → Run workflow 中填写已有标签（如 `v0.1.0`）重试。重试使用该 Release 的六个平台 VSIX 附件，并跳过商店中已发布的版本。
