# 架构

[English](architecture.md) · [简体中文](architecture.zh-CN.md)

本文说明扩展、两个 Go 协议服务器和运行在被调试进程内的 Lua 调试器是如何组合在一起的,以及每个功能在协议上经过了哪些步骤。图使用 Mermaid,GitHub 和 VS Code 的 Markdown 预览都能渲染。

## 组件

```mermaid
flowchart LR
  subgraph vscode["VS Code"]
    ui["编辑器 · 调试界面 · 变量面板"]
    ext["扩展宿主<br/>vscode/src/extension.ts<br/>vscode-languageclient · DebugAdapterExecutable"]
    ui --- ext
  end

  subgraph lsp["bin/lua-lsp (Go)"]
    glsp["tliron/glsp 处理器<br/>internal/lsp/server.go"]
    analysis["internal/analysis<br/>tree-sitter-lua:作用域、符号、<br/>成员、测试用例、诊断"]
    probe["internal/lsp/interpreter.go<br/>版本 + 标准库探测、<br/>语法检查"]
    glsp --> analysis
    glsp --> probe
  end

  subgraph dap["bin/lua-dap (Go)"]
    dserver["google/go-dap 分发<br/>internal/dap/server.go"]
    runtime["internal/dap/runtime.go<br/>子进程、行协议、<br/>断点队列"]
    dserver --> runtime
  end

  subgraph lua["用户的 Lua 解释器 (5.1–5.5 / LuaJIT)"]
    debugger["lua/debugger.lua<br/>debug.sethook · 命令循环"]
    native["bin/lua-devtools-native<br/>native/poll.c(可选)"]
    script["用户脚本 / 测试运行器"]
    debugger -. "package.loadlib" .-> native
    debugger -- "xpcall(chunk)" --> script
  end

  i18n["internal/i18n<br/>en · zh-cn"]

  ext == "LSP,stdio" ==> glsp
  ext == "DAP,stdio" ==> dserver
  probe -- "lua -e(子进程)" --> lua
  runtime == "stdin:命令<br/>协议文件:回复 + 事件<br/>stdout/stderr:程序输出" ==> debugger
  glsp -.-> i18n
  dserver -.-> i18n
```

| 部分 | 职责 |
|---|---|
| `vscode/src/extension.ts` | 只做粘合:启动语言客户端,注册调试适配器工厂、F5 配置提供者、CodeLens 调用的 Run/Debug 命令、解释器选择(状态栏)和表 JSON 视图。 |
| `cmd/lua-lsp`、`internal/lsp` | 语言服务器。用 `internal/analysis` 解析每个打开的文档,按需探测所选解释器,请求都基于最新一次解析结果回答。 |
| `internal/analysis` | tree-sitter 解析、作用域树、符号解析、静态成员推断、LuaUnit/Busted 测试发现、语法诊断。内部使用字节偏移,UTF-16 转换在 `position.go`。 |
| `cmd/lua-dap`、`internal/dap` | 调试适配器。把 DAP 请求翻译成 `debugger.lua` 的行协议命令,把它的事件翻译回 DAP 事件。 |
| `vscode/lua/debugger.lua` | 在用户的解释器里、被调试进程内运行。安装 `debug.sethook`,维护断点,用 `debug` 库遍历栈、求值表达式。 |
| `native/poll.c` | 可选的、与 Lua 版本无关的 C 模块:报告 stdin 是否可读,让程序运行中也能收到命令。 |
| `internal/i18n`、`vscode/l10n` | Go 服务器和扩展的中英文文案。 |

除了 Go 二进制、Lua 文件和原生辅助模块,扩展不打包任何东西:语言服务器和调试器都运行用户选择的解释器(`luaDevtools.luaPath`,否则依次尝试 `lua5.4` → Homebrew `lua@5.4` → `lua`)。

## 语言服务器(LSP)

### 能力

| 能力 | 数据来源 |
|---|---|
| 文本同步(增量) | `didOpen` / `didChange` / `didClose` 维护文档文本并立即重新解析 |
| 诊断(推送) | 有解释器时用解释器做语法检查,否则用 tree-sitter 的 `ERROR` / `MISSING` 节点和未闭合结构启发式 |
| 补全(`.` 和 `:` 触发) | 静态推断的成员 + 探测到的标准库;其他位置给可见符号、内置函数、关键字 |
| 转到定义 | 先查跨文件的成员和 `require` 定义(`ImplementationAt`),再查词法符号(`SymbolAt`) |
| 悬停 | 符号签名和定义行、内置函数文档,或「未定义的全局变量」 |
| 文档符号 | 作用域大纲:函数、字段(`function M.f` / `M:f`)、局部变量、全局变量 |
| CodeLens | 第 1 行的 `Run` / `Debug`;LuaUnit 和 Busted 用例上方的 `Run Test` / `Debug Test` |

### 启动与文档生命周期

```mermaid
sequenceDiagram
  participant VS as VS Code
  participant Ext as 扩展
  participant LSP as lua-lsp
  participant Lua as Lua 解释器

  VS->>Ext: 激活(打开了 .lua 文件)
  Ext->>LSP: 启动 bin/lua-lsp,initialize<br/>initializationOptions:luaPath、useInterpreter = 工作区受信任、workspaceRoots
  alt 受信任的工作区
    LSP->>Lua: lua -e <标准库探测>(去掉 LUA_INIT / LUA_PATH / LUA_CPATH,2 秒超时)
    Lua-->>LSP: _VERSION、_G 名字、string/table/io/… 的成员(LuaJIT 还有 bit、jit)
  end
  LSP-->>Ext: capabilities、serverInfo.version
  VS->>LSP: textDocument/didOpen
  LSP->>LSP: analysis.Parse(tree-sitter),同步完成
  LSP->>LSP: 启动 200 ms 诊断计时器
  VS->>LSP: textDocument/didChange(增量编辑)
  LSP->>LSP: 应用编辑、重新解析、重置计时器
  Note over LSP: 计时器触发
  alt 有可用解释器
    LSP->>Lua: lua -e <语法探测>,源码走 stdin<br/>load(source, "@document", "t", {}),不会执行
    Lua-->>LSP: "" 或 "document:行号: 信息"
  else
    LSP->>LSP: tree-sitter 诊断(回退)
  end
  LSP-->>VS: textDocument/publishDiagnostics(期间文档变了则丢弃)
  VS->>LSP: completion / hover / definition / documentSymbol / codeLens
  LSP-->>VS: 在同一把锁下基于最新解析结果回答
```

修改 `luaDevtools.luaPath` 或授予工作区信任会重启客户端,新的解释器由此被探测。不受信任的工作区从不启动解释器,只用内置的 Lua 5.4 分析。

### 诊断流水线

```mermaid
flowchart TD
  edit["didChange"] --> parse["analysis.Parse(tree-sitter)"]
  parse --> ts["tree-sitter 诊断<br/>ERROR / MISSING 节点、<br/>未闭合块及其起始行"]
  parse --> timer{"200 ms 内<br/>没有新编辑?"}
  timer -- 否 --> edit
  timer -- 是 --> hasRt{"探测到解释器?"}
  hasRt -- 否 --> publishTs["发布 tree-sitter 诊断"]
  hasRt -- 是 --> run["启动 lua -e 语法探测<br/>stdin = 文档文本"]
  run --> ok{"2 秒内<br/>成功返回?"}
  ok -- 否 --> publishTs
  ok -- "是,输出为空" --> clear["发布 []"]
  ok -- "是,document:行号: 信息" --> publishRt["在该行发布一条错误<br/>source = lua-devtools (Lua 5.x)"]
```

解释器检查一旦运行就是权威结果:Lua 自己的解析器给出标准信息(`'end' expected (to close 'function' at line 3) near <eof>`)。它遇到第一个错误就停止,所以每个文档最多显示一条解释器诊断。

### 补全

```mermaid
flowchart TD
  req["textDocument/completion,光标偏移"] --> sc{"在字符串<br/>或注释里?"}
  sc -- 是 --> empty["[]"]
  sc -- 否 --> member{"'.' 或 ':' 前<br/>有接收者?"}
  member -- 否 --> globals["该位置可见的符号<br/>+ 解释器已知的内置函数<br/>+ 关键字(5.5 含 global)"]
  member -- 是 --> repair{"解析有错误?"}
  repair -- 是 --> reparse["在光标处插入占位调用重新解析,<br/>保住外层函数的作用域"]
  repair -- 否 --> infer
  reparse --> infer["analysis.KnownMembers(receiver)"]
  infer --> sources["表构造器 · M.x = … · function M.f()<br/>别名 · 表值的 __index ·<br/>返回表字面量的函数 ·<br/>require('mod') → 工作区根下的<br/>?.lua / ?/init.lua(从不执行)"]
  sources --> stdlib{"接收者是<br/>标准库?"}
  stdlib -- 是 --> probed["解释器探测到的成员<br/>(string.、table.、bit.、jit.、…)"]
  stdlib -- 否 --> items
  probed --> items["CompletionItems<br/>':' 只保留函数"]
```

`moduleResolver` 优先使用扩展传入的当前环境搜索路径（项目依赖目录、解释器 `package.path`），再在所属工作区根目录和源文件的各级父目录下查找 `?.lua` 和 `?/init.lua`。路径按工作区隔离，未保存的打开文档优先于磁盘内容,并限制文件大小和依赖深度。这样找到的定义同时支撑跨文件的转到定义。

### CodeLens

```mermaid
flowchart LR
  doc["已解析的文档"] --> top["第 1 行:Run · Debug<br/>→ luaDevtools.run / luaDevtools.debug (uri)"]
  doc --> lu{"require 了 'luaunit'?"}
  lu -- 是 --> luTests["全局 test* / Test* 函数<br/>以及 TestX:testY 方法<br/>→ luaDevtools.runTest / debugTest (uri, name, 'luaunit')"]
  doc --> bu["describe / context / insulate / expose 内的<br/>it / spec / test<br/>→ luaDevtools.runTest / debugTest (uri, fullName, 'busted')"]
```

服务器只给出命令名;扩展通过启动调试会话来执行它们(Run 用 `noDebug`)。LuaUnit 以 `arg[1]` 收到测试名;Busted 通过 `lua/busted-runner.lua` 运行,`--filter` 经过转义并锚定,断点仍然落在原 spec 文件里。

## 调试适配器(DAP)

### 进程与通道

```mermaid
flowchart LR
  vs["VS Code 调试界面"]
  dap["lua-dap"]
  lua["lua debugger.lua program.lua args…"]
  file[("LUA_DEVTOOLS_PROTOCOL<br/>临时文件,每行一个 JSON")]
  vs <== "DAP(Content-Length 分帧),stdio" ==> dap
  dap -- "stdin:{id, cmd, …} 命令<br/>有界队列 + 写入 goroutine" --> lua
  lua -- "追加回复 {id, body | error}<br/>和事件 {event, …}" --> file
  file -- "每 5 ms 尾随读取直到进程退出" --> dap
  lua -- "stdout / stderr:只有程序输出" --> dap
  dap -- "OutputEvent(按 UTF-8 边界切分)" --> vs
```

三条通道把协议和程序的输入输出分开:

- **stdin → Lua**:命令。暂停时 Lua 阻塞读取;有原生辅助模块时也能在调试 hook 里读取(先 `poll_stdin()`,所以读取不会阻塞)。
- **协议文件 → 适配器**:回复和事件。用普通文件而不是继承的管道,是为了在 Windows 上也能配合外部 Lua;适配器尾随读取直到进程退出。
- **stdout/stderr → 适配器**:原样的程序输出,转发成 `output` 事件。`print`、`io.write`、`io.stdout:write` 和 C 模块的行为与调试器外完全一致。

### 一次调试会话

```mermaid
sequenceDiagram
  participant VS as VS Code
  participant DAP as lua-dap
  participant Lua as debugger.lua

  VS->>DAP: initialize
  DAP-->>VS: capabilities(configurationDone、条件断点、悬停求值)
  DAP-->>VS: initialized
  VS->>DAP: setBreakpoints(launch 之前)
  DAP-->>VS: verified = false,「将在下次暂停时生效」(暂存在按需创建的 Runtime 里)
  VS->>DAP: launch {program, args, cwd, env, luaPath, packagePath, stopOnEntry, noDebug}
  DAP->>Lua: 启动解释器,带 LUA_DEVTOOLS_PROTOCOL、LUA_PATH / LUA_CPATH
  Lua->>Lua: 加载 json.lua、snapshot.lua,尝试 package.loadlib(native)
  DAP->>Lua: capabilities
  Lua-->>DAP: {liveControl: 原生模块是否加载}
  DAP->>Lua: setBreakpoints(下发暂存的断点)
  Lua-->>DAP: verified = true
  DAP-->>VS: breakpoint 事件(changed)、launch 响应
  VS->>DAP: configurationDone
  DAP->>Lua: run {stopOnEntry, noDebug, cwd}
  Lua->>Lua: 安装 hook,xpcall(主 chunk)
  Note over Lua: line hook 命中断点
  Lua-->>DAP: 事件 stopped {threadId, reason, file, line}
  DAP-->>VS: stopped
  VS->>DAP: threads / stackTrace / scopes / variables / evaluate
  DAP->>Lua: threads / stack / scopes / variables / evaluate
  Lua-->>DAP: 协程列表、栈帧、locals + upvalues、表引用、求值结果
  DAP-->>VS: 响应
  VS->>DAP: next / stepIn / stepOut / continue
  DAP->>Lua: 同名命令(先把运行时标记为运行中)
  Note over Lua: … 脚本结束或出错 …
  Lua-->>DAP: 输出 traceback(出错时)· 进程退出
  DAP-->>VS: exited {exitCode}、terminated
```

`stopOnEntry` 实现为从第一行开始的 step-in。`noDebug`(Run Without Debugging)完全不安装 hook,运行时错误只打印 traceback 并以退出码 1 结束。

### 被调试进程的状态机

```mermaid
stateDiagram-v2
  [*] --> Handshake: lua debugger.lua 启动
  Handshake --> WaitingForRun: commandLoop() 回答 capabilities / setBreakpoints
  WaitingForRun --> Running: run
  Running --> Paused: line hook:断点 · 步进目标 · 收到 pause
  Running --> Exception: 错误传到 xpcall 处理器(栈仍完整)
  Exception --> Paused: pause(reason = exception),noDebug 时跳过
  Paused --> Running: continue / next / stepIn / stepOut
  Paused --> [*]: 适配器关闭 stdin
  Running --> Draining: count hook(每 1000 条指令)<br/>或每 100 次 line 事件,仅原生模式
  Draining --> Running: 处理最多 32 条待处理的 pause / updateBreakpoints
  Running --> [*]: 主 chunk 返回 · os.exit
  Exception --> [*]: traceback 写到 stderr,退出码 1
```

`hook(event, line)` 内部:

1. 调试器自己执行代码时(`inDebugger`)忽略 hook,所以求值表达式不会重入调试器。
2. 有原生模块时,每个 count 事件或每 100 次 line 事件排空一次 stdin:运行中只接受 `pause` 和 `updateBreakpoints`。
3. 跳过 `debugger.lua` 自身的栈帧;chunk 的 `source` 通过适配器规范化一次(`resolvePath` 事件 → `resolvedPath` 回复),这样符号链接和 `./` 前缀不影响断点路径匹配。
4. `shouldStop` 检查断点(条件在命中的栈帧里求值;条件求值失败也会停下并报告)和步进模式。`next` 和 `stepOut` 只比较当前协程的栈深度。
5. `pause(reason, info)` 发送 `stopped`,然后进入阻塞的命令循环,直到收到恢复命令。

### 程序运行中的断点更新

```mermaid
flowchart TD
  req["VS Code 发来 setBreakpoints"] --> paused{"Lua 已暂停?"}
  paused -- 是 --> direct["发送 setBreakpoints 并等待回复<br/>verified = true"]
  paused -- 否 --> live{"原生模块<br/>已加载?"}
  live -- 是 --> notify["入队 updateBreakpoints(不等回复)<br/>响应 verified = false,「待生效」"]
  notify --> hook["Lua 在下一个 hook 应用,<br/>发送 breakpointsChanged"]
  hook --> event["breakpoint 事件 → verified = true"]
  live -- 否 --> queue["按文件记住<br/>响应 verified = false,「将在下次暂停时生效」"]
  queue --> stop["下一个 stopped 事件时:<br/>先下发队列,再转发 stopped"]
```

`pause` 走同样的分支:有原生模块时是一条不等回复的 `pause` 命令,下一个 hook 会响应它(reason 为 `pause`);没有时请求失败并给出说明。运行中发出的命令从不等待回复,所以程序卡在 C 调用里也不会阻塞适配器的请求循环——`disconnect` 和 `terminate` 仍然可用。

### 协程、栈帧与变量

- `coroutine.create` / `coroutine.wrap` 被包装,为新线程安装 hook 并登记;`coroutine.resume` 被包装,使得跨过 `yield` 的步进回到调用方继续。`threads` 列出存活的协程(`1` 为主线程)。
- 栈帧 id:活动栈用相对 hook 的 `1..n`;挂起的协程用 `threadId × 1 000 000 + level`,因此 `scopes` / `variables` / `evaluate` 能定位任意已列出协程的栈帧。
- 作用域为 `Locals`(`debug.getlocal`,跳过临时值)和 `Upvalues`(`debug.getupvalue`);表获得 `variablesReference`,展开时最多显示 200 个排序后的条目。引用在每次暂停时重建。
- `evaluate` 先按 `return <表达式>` 编译,再按语句编译,环境的 `__index` / `__newindex` 依次解析局部变量、upvalue、`_G`——所以在调试控制台里赋值会改变暂停中的程序。
- 自定义请求 `lua/snapshot {variablesReference}` 返回表的 JSON 快照(`snapshot.lua` 策略:深度 32、10 000 个值、512 KiB、不触发元方法);扩展在只读的 `lua-table:` 文档里展示。

### 原生轮询辅助模块

`native/poll.c` 只导出一个函数 `poll_stdin()`,只用到 `lua_createtable`、`lua_pushcclosure`、`lua_setfield`、`lua_pushboolean` 四个 API,它们的签名从 Lua 5.1 到 5.5 以及 LuaJIT 完全一致。macOS/Linux 上符号在 `dlopen` 时从宿主进程解析;Windows 上模块枚举已加载的模块,选出导出 Lua API 的那个,无论它的 DLL 叫什么名字(如果有两个则拒绝加载)。`debugger.lua` 用 `package.loadlib` 从服务器旁边的 `bin/` 加载它,支持 `LUA_DEVTOOLS_NATIVE=<路径>` 或 `off`,把 stdin 切换为无缓冲以保证内核的就绪状态和 `read` 的结果一致,任何一步失败都静默退回纯 Lua。

## 构建与发布

```mermaid
flowchart LR
  src["Go + C + TypeScript + Lua 源码"] --> bg["scripts/build-go.mjs<br/>go build ./cmd/…(cgo tree-sitter)<br/>+ build-native.mjs(cc -shared)"]
  src --> es["vscode/esbuild.mjs<br/>dist/extension.js"]
  bg --> bin["vscode/bin/&lt;target&gt;/<br/>lua-dap · lua-lsp · lua-devtools-native"]
  bin --> pkg["scripts/package.mjs<br/>vsce package --target"]
  es --> pkg
  pkg --> vsix["lua-devtools-&lt;target&gt;-&lt;version&gt;.vsix<br/>darwin/linux/win32 × x64/arm64"]
  vsix --> rel["release.yml 在 GitHub Release 时:<br/>Marketplace + Open VSX"]
```

CI(`ci.yml`)在匹配的 runner 上构建每个目标(cgo 不能交叉编译),运行 `go test -race -tags=integration`、走 stdio 的 LSP/DAP 冒烟测试和 VS Code 端到端测试,然后打包。另有独立任务从源码构建 Lua 5.1–5.5 和固定版本的 LuaJIT,分别在加载原生模块和 `LUA_DEVTOOLS_NATIVE=off` 两种模式下跑调试器测试,保证纯 Lua 回退路径始终被覆盖。

## 从哪里看起

| 问题 | 文件 |
|---|---|
| DAP 请求如何变成 Lua 命令 | `internal/dap/server.go` → `internal/dap/runtime.go` |
| 命令、回复、事件的线上格式 | `vscode/lua/debugger.lua` 的头部注释,`runtime.go` 里的 `message` 结构 |
| 断点为什么显示「待生效」或「将在下次暂停时生效」 | `Runtime.SetBreakpoints`、`flushPendingBreakpoints` |
| 补全列表如何组装 | `internal/lsp/server.go`(`completion`)、`internal/lsp/completion.go`、`internal/analysis/members.go` |
| 解释器探测 | `internal/lsp/interpreter.go`(`libraryProbe`、`syntaxProbe`) |
| 测试发现 | `internal/analysis/luaunit.go`、`internal/analysis/busted.go` |
| 扩展接线、命令、设置 | `vscode/src/extension.ts`、`vscode/package.json` |

## 扩展语言与调试服务

`analysis/refactoring.go` 根据词法绑定查找引用并重命名局部符号，双向检查名称捕获；LSP 返回带文档版本的编辑。`analysis/signature.go` 静态解析被调用函数和参数位置，并复用有界模块解析器提供跨模块函数声明。

上面的传输图描述普通 launch。可选 `transport.lua` 使用 LuaSocket TCP 支持 attach 和交互式 launch。交互式 launch 创建带认证的本机回环连接，保留 stdin，通过有界输入队列传入程序数据。attach 连接主动接入的宿主，断开会关闭传输并恢复钩子、协程 API 和 JIT 状态，不杀死宿主。详见[调试指南](debugging.zh-CN.md)。

`lua/resumeCoroutine` 不带参数地恢复挂起协程，在断点、yield 或结束时再次停止；可选的捕获错误检查保留失败协程栈直到继续。复杂 table 快照使用 `lua-table-v1` 表示对象身份和带类型的键，渲染只读文档前仍执行深度、数量和字节上限检查。
