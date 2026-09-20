# 调试示例 / Debugging examples

安装扩展后，在 VS Code 中打开此目录，选择 Lua 解释器，打开一个 `.lua` 文件并按 F5。使用 **Debug current Lua file**；也可在仓库的扩展开发窗口中运行。建议先阅读文件顶部注释，再在标注的位置设置断点。

Open this directory in VS Code with the extension installed, select a Lua interpreter, open a `.lua` file and press F5 using **Debug current Lua file**. Comments in each file suggest breakpoints. The examples also work in the extension development host.

| 文件 / File | 观察内容 / What to try |
|---|---|
| [hello.lua](hello.lua) | 入门断点、局部变量 / Basic breakpoints and locals |
| [step.lua](step.lua) | 函数单步、嵌套表 / Step into/out of functions, inspect tables |
| [require_local.lua](require_local.lua) | 本地 `lib`、目录模块、跨文件单步 / Local modules and cross-file stepping |
| [require_system.lua](require_system.lua) | 标准库和已安装库，Lua/C 实现对比 / Built-in and installed libraries, Lua vs C |
| [error.lua](error.lua) | 故意触发未捕获异常 / Deliberate uncaught exception |
| [sleep.lua](sleep.lua) | 阻塞 sleep 前后暂停 / Break before and after a blocking sleep |
| [coroutine.lua](coroutine.lua) | `create`、`resume`、`yield`、`wrap`，生产/消费 / Producer and consumer |
| [coroutine_sleep.lua](coroutine_sleep.lua) | 两个协程交替运行，分别等待 / Two workers with cooperative timed waits |
| [coroutine_error.lua](coroutine_error.lua) | `resume` 捕获错误，调用者继续运行 / Captured coroutine errors |
| [runtime_control.lua](runtime_control.lua) | 运行中主动暂停、增删断点和条件断点 / Pause and edit breakpoints while running |
| [test_luaunit.lua](test_luaunit.lua) | LuaUnit 单用例运行/调试 CodeLens / Per-test Run/Debug |
| [example_spec.lua](example_spec.lua) | Busted 单用例运行/调试 CodeLens / Per-test Run/Debug |

## Require 与库跳转 / Require and library navigation

`require_local.lua` 加载本地 [lib/cart.lua](lib/cart.lua)，后者再加载目录模块 [lib/money/init.lua](lib/money/init.lua)。示例根据脚本路径补充 `package.path`，因此从仓库根目录或 `examples/` 运行都能找到本地库，不会覆盖原有搜索路径。对 `cart.checkout(items)` 单步进入（F11），可观察库内局部变量、嵌套调用和调用栈；也可以直接在两个库文件中打断点。也可在 `cart.checkout`、`money.format` 上按 F12 或 Ctrl/⌘ 点击，直接跳到本地库的函数实现。重复 `require` 返回已缓存的模块，不会重新执行模块顶部代码。

`require_local.lua` loads `lib/cart.lua`, which loads the directory module `lib/money/init.lua`. It adds script-relative entries to `package.path` while preserving existing entries. Step into `cart.checkout(items)` with F11, or set breakpoints in either library. Repeated `require` calls use the cached module rather than rerunning its initialization.

`require_system.lua` 演示两类“系统库”：内置标准库 `require("math")`，以及安装到所选解释器搜索路径中的 `require("socket")` / `require("socket.url")`。这里的已安装库也可以位于用户级 LuaRocks 目录，不要求系统全局安装。`url.parse` 是 Lua 实现，源码可用时能 F11 进入；`math.sqrt` 和 `socket.gettime` 是 C 实现，只能在调用前后调试。示例仅解析 URL，不访问网络。编辑器 F12 支持工作区内可静态解析的本地模块函数；工作区外已安装库的搜索路径和 C 函数实现暂不支持 F12，调试时仍可用 F11 进入可用的 Lua 源码。

`require_system.lua` uses the built-in `math` library and installed LuaSocket modules from the selected interpreter's search paths, including user-level installations. F11 can enter the Lua implementation of `url.parse` when its source is available, but cannot enter C implementations such as `math.sqrt` or `socket.gettime`. The example makes no network requests. F12 supports statically resolved local module functions inside the workspace. Installed libraries outside the workspace and C implementations are not yet supported by F12; debugger F11 can still enter available Lua source.

```sh
lua5.4 examples/require_local.lua
lua5.4 examples/require_system.lua
```

## Sleep 与依赖 / Sleep and dependencies

Lua 标准库没有 `sleep`。`sleep.lua`、`coroutine_sleep.lua` 和 `require_system.lua` 使用 **LuaSocket**，需安装到所选解释器可加载的位置，例如 Lua 5.4：

Lua has no standard `sleep` function. Both sleep examples and `require_system.lua` require **LuaSocket** installed for the selected interpreter, for example Lua 5.4:

```sh
luarocks --lua-version=5.4 install luasocket
lua5.4 -e 'print(require("socket")._VERSION)'
lua5.4 examples/sleep.lua 0.2
lua5.4 examples/coroutine_sleep.lua
lua5.4 examples/coroutine.lua
lua5.4 examples/coroutine_error.lua
lua5.4 examples/runtime_control.lua 2
```

以上命令在仓库根目录执行；若已进入 `examples/`，去掉路径前缀。将 `lua5.4` 替换为你的解释器。无参数运行 `sleep.lua` 时，每次等待 2 秒，共 3 次；调试时可在 launch 配置中添加 `"args": ["0.2"]`。LuaUnit 和 Busted 示例分别需要 `luaunit` 和 `busted`。

Run these commands from the repository root; omit `examples/` when already in this directory. Replace `lua5.4` with your interpreter. By default `sleep.lua` sleeps three times for two seconds each; a launch configuration can pass `"args": ["0.2"]`. The test examples require `luaunit` and `busted`, respectively.

## 预期行为 / Expected behavior

- `socket.sleep` 是阻塞的 C 调用，期间点击暂停要等它返回 Lua 才生效。协程调度示例通过 `yield` 让出执行权，并把调度器的每次空闲等待限制为 50 ms；它不创建操作系统线程。
- `runtime_control.lua` 有意忙循环消耗 CPU，默认运行约 10 秒 CPU 时间，暂停期间不计时；它不是 sleep 的替代实现。原生辅助模块加载失败时，主动暂停不可用，断点修改需等下一次停止。
- `coroutine_error.lua` 正常退出，因为错误由 `resume` 捕获；调试器不会在出错协程内自动暂停，需提前设置断点。Lua 5.1/LuaJIT 在协程中暂停时不能检查挂起的主线程，LuaJIT 调试期间关闭 JIT。

- A blocking `socket.sleep` must return before Pause can take effect. The scheduler example yields cooperatively and limits each idle C sleep to 50 ms; it does not create OS threads.
- `runtime_control.lua` intentionally uses CPU for about ten CPU seconds by default, excluding pauses. It is not a sleep implementation. Without the native helper, Pause is unavailable and breakpoint changes wait for the next stop.
- `coroutine_error.lua` exits successfully because `resume` captures the error. Set a breakpoint before the error to inspect the coroutine; it will not stop automatically on that caught error. Lua 5.1/LuaJIT cannot inspect the suspended main thread from a stopped coroutine; LuaJIT debugging disables JIT compilation.

新增示例使用 Lua 5.1–5.5 / LuaJIT 2.1 兼容语法；第三方模块需与解释器的版本和架构匹配。

The new examples use Lua 5.1–5.5 / LuaJIT 2.1 compatible syntax. Third-party modules must match the interpreter version and architecture.
