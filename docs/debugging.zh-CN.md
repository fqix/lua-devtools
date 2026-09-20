# 调试方式

普通 F5 启动仅需 Lua。交互式 launch 和主动接入的 attach 还需要为程序使用的解释器安装 LuaSocket，可在环境树安装 `luasocket`。

## 程序输入

在 Lua launch 配置中添加 `"interactive": true`。通过命令面板中的 **Lua DevTools：发送程序输入** 发送一行，或 **Lua DevTools：关闭程序输入** 发送 EOF。程序使用自己的 stdin，调试命令通过独立且带认证的本机 TCP 连接传输。支持 `io.read`，不提供终端模拟或原始按键输入；当前会话关闭输入后无法重新打开。

## Attach 与嵌入式宿主

执行 **Lua DevTools：复制附加调试引导代码**，获取包含当前扩展安装路径的代码。在宿主开始运行待调试代码之前加载：

```lua
local session = dofile("/absolute/path/to/extension/lua/lua-devtools.lua").listen {
  host = "127.0.0.1",
  port = 8172,
  token = "my-session-token",
  timeout = 120,
}
-- 适配器连接并配置断点后，才会执行后续代码。
local function work()
  local value = 42
  return value
end
local result = session:run(work) -- 可选：在错误栈展开前检查异常
session.stop()                 -- 恢复之前的钩子和协程 API
```

先启动宿主，再使用以下 VS Code 配置：

```json
{
  "type": "lua",
  "request": "attach",
  "name": "Attach to Lua",
  "host": "127.0.0.1",
  "port": 8172,
  "token": "my-session-token",
  "cwd": "${workspaceFolder}",
  "stopOnEntry": false
}
```

模块路径、标准输出和标准输入由宿主管理。源码路径须能被 VS Code 访问并与宿主一致，暂不提供远程路径映射。`listen` 等待一个适配器连接，默认超时 120 秒。断开或停止调试会让宿主继续运行，不会杀死宿主进程；Lua 再次检测到断开时恢复钩子和协程 API，阻塞中的 C 调用需先返回。

Attach 需要宿主主动接入：嵌入式应用必须在目标 Lua state 中加载引导代码，并提供标准 `debug`、`io`、`os`、`package` 库及 LuaSocket。不支持按进程 ID 注入或调试 C 实现。`session:run` 保留返回值，异常检查结束后重新抛出错误；宿主需要恢复时可在外层使用 `pcall`。普通宿主代码支持断点和单步，但若需要在未捕获错误的栈展开前检查，请使用 `session:run`。

非本机回环地址监听必须设置 token。token 通过明文 TCP 传输；远程使用时请采用可信的加密隧道。

## 协程

暂停时使用 **Lua DevTools：恢复挂起协程**，可以在调用方保持暂停的情况下运行选中的挂起协程。该操作不传入 resume 参数，也不保留 yield／return 值，因此会改变程序流程。协程 yield、结束或命中断点后再次暂停。常规继续和单步仍遵循程序自己的调度流程。

在 launch 或 attach 中设置 `"breakOnCoroutineErrors": true`，可在 `coroutine.resume` 捕获错误后、调用方收到失败结果之前检查失败协程。继续执行前可访问保留的栈和局部变量，默认关闭。此功能不能回退或重启已失败的协程。

## 复杂 table 查看

普通表保留普通 JSON 结构。需要扩展表示的表使用 `{"$format":"lua-table-v1","root":...}`：每个表包含 `$id`、`$type: "table"` 和保存键值对的 `entries`；重复对象使用 `$ref`。二进制字符串使用 `$type: "bytes"` 和十六进制数据，非有限数值使用类型标记。稀疏数字键、混合键和对象键均保留类型。视图只读，不提供导出命令。
