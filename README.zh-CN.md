# Lua DevTools

[English](README.md) · [简体中文](README.zh-CN.md)

VS Code 的 Lua 调试与语言服务扩展，支持 **Lua 5.1–5.5 和 LuaJIT 2.1**，适用于 macOS、Linux 和 Windows。

## 快速开始

1. 从 [VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=fqix.lua-devtools) 安装扩展，或从 [Releases](https://github.com/fqix/lua-devtools/releases) 下载 VSIX。需要 VS Code 1.91 或更高版本。
2. 单独安装 Lua 解释器，通过状态栏或 **Lua DevTools → Lua 环境** 选择。发现解释器需要信任工作区。
3. 打开 `.lua` 文件，按 **F5** 调试，或点击编辑器右上角 **Run** 运行，无需配置 `launch.json`。

## 功能

- 断点、条件断点、单步、调用栈、协程检查和调试控制台求值。
- 语法诊断、补全、悬停、大纲、定义与引用、局部重命名及参数提示，支持 Lua 5.5 声明及可静态解析的本地模块。
- 解释器发现、切换和项目独立装包。
- table 只读 JSON 查看，以及 LuaUnit、Busted 单用例运行与调试。
- 中英文界面。

## 环境与装包

在 **Lua 环境** 中，点击解释器即可切换，展开查看路径，点击 **+** 手动输入路径，或刷新重新扫描。选择对后续运行和调试生效；launch 配置中的 `luaPath` 优先。

选中环境后，展开可查看标准库及函数，以及项目安装包和解释器搜索路径中的第三方模块。点击 Lua 模块可打开源码，原生模块可显示文件位置。F12 跳转和模块成员补全会使用当前解释器的 Lua 搜索路径及项目安装包；外部安装后点击刷新。扫描不执行第三方模块，也不反映运行时对 `package.path` 的修改。

点击解释器旁的 **包图标**，或 **项目安装包** 上的安装按钮，输入 `luasocket` 安装最新版，也可输入 `luaunit@3.5-1` 指定版本。**项目安装包** 显示已安装版本，点击升级按钮可升级，右键可安装其他版本或卸载当前版本；会破坏依赖关系的卸载由 LuaRocks 阻止。需要 LuaRocks；不在 PATH 中时可设置 `luaDevtools.luarocksPath`。安装日志显示在任务终端。

依赖存放在 `.lua-devtools/rocks/`，按项目和解释器隔离，新的运行与调试会话会自动加载。请将 `.lua-devtools/` 加入 `.gitignore`。原生包可能需要编译器和 Lua 开发头文件；外部终端的搜索路径不会自动修改。

## 查看表与运行测试

调试暂停时，右键变量面板中的 table，选择 **查看表（JSON）**。快照保留数值精度，函数、userdata 和 thread 显示类型标记。循环引用、混合／稀疏键、对象键、二进制字符串和非有限数值会使用带 `$format: "lua-table-v1"` 的标记结构。上限为 32 层、10,000 个值、512 KiB。

为所选解释器安装 `luaunit` 或 `busted` 后，可在静态识别的用例上点击 **运行测试／调试测试**。LuaUnit 文件需调用自身 runner；Busted 用例使用扩展提供的 runner。参见[示例指南](examples/README.md)。

## 格式化

安装 [StyLua](https://github.com/JohnnyMorganz/StyLua)，或设置 `luaDevtools.styluaPath`，在受信任工作区中使用 **格式化文档**。遵循项目的 `stylua.toml` / `.stylua.toml` 和 `.styluaignore`；语法支持取决于所安装的 StyLua 版本，失败时保留原文。

保存时自动格式化：

```json
"[lua]": {
  "editor.defaultFormatter": "fqix.lua-devtools",
  "editor.formatOnSave": true
}
```

## 配置

| 设置 | 用途 |
|---|---|
| `luaDevtools.luaPath` | 解释器路径；自动查找依次尝试 `lua5.4`、Homebrew `lua@5.4`、`lua` |
| `luaDevtools.luarocksPath` | LuaRocks 可执行文件；默认从 PATH 查找 `luarocks` |
| `luaDevtools.trace.server` | 语言服务日志：`off`、`messages` 或 `verbose` |

自定义 `launch.json` 时，设置 `type: "lua"`、`request: "launch"` 和 `program`。可选字段包括 `args`、`cwd`、`env`、`luaPath`、`stopOnEntry`、`packagePath`、`packageCPath`。

## 更多调试方式

支持 TCP attach 到主动加载调试器的 Lua／嵌入式宿主，断开后宿主继续运行。launch 配置设置 `interactive: true` 后，可通过命令面板发送标准输入或 EOF；这两种模式需要 LuaSocket。暂停时可独立恢复挂起协程，`breakOnCoroutineErrors: true` 可检查 `coroutine.resume` 捕获的错误栈。配置和使用方法见[调试指南](docs/debugging.zh-CN.md)。

## 限制

- 普通 launch 的主动暂停和运行中更新断点依赖内置原生辅助模块；TCP 模式通过 LuaSocket 轮询。阻塞中的 C 调用需返回 Lua，不能用 F11 进入 `os.time`、`cjson.encode` 等 C 实现。
- LuaJIT 调试时禁用 JIT 编译。attach 需要宿主主动接入，不支持注入任意运行中的进程。
- 引用查询限当前文件；重命名限词法局部变量、参数和函数，并拒绝会改变绑定的名称。参数提示基于静态函数声明。
- 语言分析基于静态源码，不解析动态加载器、运行时对 `package.path` 的修改、仅在 launch 中配置的搜索路径或原生 C 实现。缺少受信任且可用的解释器时，回退到 Lua 5.4 分析。

## 文档

- [示例指南](examples/README.md)
- [开发、测试与发布](docs/development.zh-CN.md)
- [架构说明](docs/architecture.zh-CN.md)
- [更新记录](vscode/CHANGELOG.md)
