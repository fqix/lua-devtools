# Lua DevTools

[English](README.md) · [简体中文](README.zh-CN.md)

VS Code 的 Lua 调试与语言服务扩展。协议服务端是独立的 Go 二进制，扩展只负责把它们接入 VS Code。

```
VS Code ──DAP──▶ vscode/bin/lua-dap (Go, google/go-dap) ──行协议──▶ vscode/lua/debugger.lua (debug.sethook)
VS Code ──LSP──▶ vscode/bin/lua-lsp (Go, tliron/glsp + tree-sitter-lua)
```

## 功能

- 调试：断点、条件断点、单步（next / step in / step out）、调用栈、局部变量与 upvalue、表格展开、Debug Console 求值（可赋值）、运行时错误处暂停、`stopOnEntry`、不调试直接运行。
- 语言服务：语法诊断（缺少闭合符号时指出对应起始行）、文档大纲、跳转定义、补全（含标准库及可静态确定的表成员）、悬停、文件顶部 `Run | Debug` CodeLens。
- 编辑器右上角 Run / Debug 按钮；无 launch.json 时对当前 `.lua` 文件直接 F5。
- 支持 `coroutine.create` / `coroutine.wrap` 中的断点和单步；提供协程列表，可查看挂起协程的调用栈、局部变量和 upvalue，并在选中栈帧中求值或赋值。
- 成员补全支持表别名、表形式的 `__index`、简单函数返回的表字面量，以及本地 `require` 模块的顶层导出。
- F12 / Ctrl/⌘ 点击可跳到本地 `require` 导出函数的实现，支持可静态解析的别名、方法、目录模块和未保存源码。
- 支持 Lua 5.1–5.5 和 LuaJIT 2.1 调试；在受信任的工作区中，语法检查和标准库补全跟随 Lua 5.1–5.5 和 LuaJIT 2.1 解释器版本。
- Lua 5.5 声明：支持 `global` / `global function` 的跳转和大纲、词法遮蔽、前置属性及具名可变参数（`...args`）；`global *` / `global <const> *` 保留隐式全局查找，不生成虚假的大纲符号。
- 程序 stdout/stderr 与调试协议分离，支持直接 `io.stdout:write` 及不带换行的输出。
- 中英文界面。

## 原生轮询辅助模块

扩展不自带 Lua 解释器。每个 OS/架构安装包包含一个可选的 C 模块，仅使用四个稳定的 Lua C API，不链接指定版本的 liblua。macOS/Linux 从宿主进程解析符号；Windows 从已加载模块查找导出，不依赖 DLL 名称。解释器必须允许动态加载并导出所需符号；Windows 检测到多个 Lua API 提供者时也会回退。模块缺失或加载失败时自动使用纯 Lua 调试。

模块提供 stdin 就绪检测，让调试器在运行中接收暂停和断点修改。解释器版本感知的诊断和标准库补全支持 Lua 5.1–5.5 和 LuaJIT 2.1，包括 Lua 5.1 环境函数和 LuaJIT 的 `bit` / `jit` 库。

## 测试 CodeLens

导入 `luaunit` 的测试文件中，全局 `test*`／`Test*` 函数和 `function TestFoo:testBar()` 等方法上方会显示 **运行测试／调试测试**。文件需调用 LuaUnit runner 并接受其标准命令行参数，例如 `os.exit(lu.LuaUnit.run())`。点击时传入精确用例名，保留 setup／teardown 流程。

Busted 支持 `describe`、`context`、`insulate`、`expose` 下的 `it`、`spec`、`test`，也支持顶层用例。筛选使用转义并完整匹配的用例全名；runner 在所选 Lua 解释器内执行，断点仍设置在原 spec 文件中。工作目录为所属工作区根目录（工作区外为 spec 所在目录），会读取该目录的 `.busted` 配置，但忽略其中的 `lua` 选项，以保留当前解释器和调试器。

框架需安装到 `luaDevtools.luaPath` 所选解释器可加载的位置，扩展不内置测试框架。参见 [LuaUnit 用例选择](https://luaunit.readthedocs.io/en/latest/3_getting-started.html#using-the-command-line) 和 [Busted 安装说明](https://lunarmodules.github.io/busted/#usage)。示例见 `examples/test_luaunit.lua` 和 `examples/example_spec.lua`。

当前仅静态识别：不为局部 LuaUnit 测试表、动态生成的用例或名称、自定义 DSL 别名、全名重复的 Busted 用例提供入口。文件级运行／调试入口保留；未内置 runner 的 Busted spec 请使用单用例入口。

## 要求

- VS Code ≥ 1.91
- 运行脚本的机器上装有 Lua 5.1–5.5 或 LuaJIT 2.1（`brew install lua@5.4`、`apt install lua5.4` 等）

## 配置

打开 Lua 文件后，点击状态栏的解释器版本，或运行 **Lua DevTools: 选择解释器**。列表会发现 PATH 和常见 Homebrew 安装中的 Lua 5.1–5.5 / LuaJIT 2.1，也可输入绝对路径或 PATH 中的命令，或恢复自动检测。选择保存到工作区的 `luaDevtools.luaPath`（未打开工作区时保存到用户设置），并自动重启语言服务；之后的运行和调试使用该解释器。多根工作区共用此选择；launch 配置中的 `luaPath` 仍优先。不受信任的工作区需先授予信任才能探测解释器。

| 设置 | 说明 |
|---|---|
| `luaDevtools.luaPath` | Lua 解释器路径；为空时依次查找 `lua5.4`、Homebrew `lua@5.4`、`lua` |
| `luaDevtools.trace.server` | `off` / `messages` / `verbose`，在 Output 面板查看 JSON-RPC 报文 |

同时安装多个 Lua 版本时，通过 `luaDevtools.luaPath` 选择语言服务和调试共用的解释器；launch 配置中的 `luaPath` 只覆盖该调试会话。自动查找仍优先使用 Lua 5.4；只有 `lua5.2`、`lua5.3` 或 `lua5.5` 名称的可执行文件需要显式指定。修改设置后语言服务自动重启。

在受信任的工作区中，语法检查和标准库补全跟随所选 Lua 5.1–5.5 或 LuaJIT 2.1 解释器。检查只编译文档，不执行文档代码，并禁用 `LUA_INIT`；解释器不可用或工作区不受信任时，回退到内置 Lua 5.4 分析。

静态 `require("foo.bar")` 补全查找所属工作区根目录下的 `foo/bar.lua` 或 `foo/bar/init.lua`；工作区外的文件以自身目录为根。优先读取已打开文档的未保存内容，不执行模块。

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

CodeLens 端到端测试需要为 Lua 5.4 安装 `luaunit` 3.4 和 `busted` 2.2.0。使用自定义 LuaRocks 目录时，先导出对应的 `LUA_PATH` 和 `LUA_CPATH`，再运行 `npm run test:e2e`；CI 会将这些依赖安装到临时目录。

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

CI 打包 darwin-x64/arm64、linux-x64/arm64 和 win32-x64/arm64。Windows ARM64 使用 LLVM-MinGW 编译 cgo；该平台不支持 Go race detector，因此运行普通测试。

修改 `vscode/package.json` 的 `version`，打 `v<version>` 标签并发布 GitHub Release。`release.yml` 复用 CI 构建，发布 VSIX 并挂到 Release。仓库需配置环境 `marketplace-publish` 及密钥 `AZURE_CLIENT_ID`、`AZURE_TENANT_ID`（Marketplace 工作负载身份联合）和 `OVSX_PAT`。

发布失败后，可在 Actions → Release → Run workflow 中填写已有标签（如 `v0.1.0`）重试。重试使用该 Release 的六个平台 VSIX 附件，并跳过商店中已发布的版本。

## 已知限制

- **运行中控制**：主动暂停和断点更新在下一个 Lua 调试 hook 处理，无法中断阻塞中的 C 函数或系统调用。原生模块加载失败时，主动暂停不可用，断点变更延迟到下次暂停。不支持程序交互式标准输入。
- **协程执行控制**：不能对非当前停止的协程单步或独立恢复其他挂起协程。被 `coroutine.resume` 捕获的错误不会在出错协程内暂停。不支持替换调试器的 hook。Lua 5.1 / LuaJIT 暂停在协程中时无法检查挂起的主线程；LuaJIT 调试期间关闭 JIT 编译。
- **接入方式**：不支持 attach 到已有进程，也不支持嵌入式 Lua。
- **复杂类型推断**：不合并控制流，不推断函数形式的 `__index` 或复杂函数返回值。
- **模块解析**：不解析嵌套导出、自定义 `package.path`、C 模块或动态加载器；解析受文件大小和依赖深度限制。
- **错误诊断**：解释器诊断为英文，每次报告首个语法错误并定位到行，不提供自动修复；回退分析的部分错误仍为通用描述。

## 计划

- 更完整的控制流与返回值推断、未定义全局变量警告、自定义模块搜索路径和嵌套导出解析。
- attach 和嵌入式 Lua 接入。
