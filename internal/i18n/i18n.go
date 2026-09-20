// Package i18n translates the few user-visible strings the Go servers produce.
// Both DAP and LSP clients send their UI locale in the initialize request.
package i18n

import (
	"fmt"
	"strings"
)

// Normalize maps a client locale such as "zh-CN" or "zh_Hans" to a catalog key.
func Normalize(locale string) string {
	l := strings.ToLower(strings.ReplaceAll(locale, "_", "-"))
	switch {
	case strings.HasPrefix(l, "zh"):
		return "zh-cn"
	default:
		return "en"
	}
}

// T returns the message for key in the given (normalized) locale, formatted with args.
// Unknown keys and locales fall back to English, then to the key itself.
func T(locale, key string, args ...any) string {
	msg, ok := catalogs[locale][key]
	if !ok {
		msg, ok = catalogs["en"][key]
	}
	if !ok {
		msg = key
	}
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

var catalogs = map[string]map[string]string{
	"en": {
		// lsp
		"codelens.runTest":      "$(beaker) Run Test",
		"codelens.debugTest":    "$(debug-alt) Debug Test",
		"codelens.run":          "$(play) Run",
		"codelens.debug":        "$(debug-alt) Debug",
		"hover.definedOnLine":   "defined on line %d",
		"hover.globalUndefined": "not defined in this file",
		"completion.keyword":    "keyword",
		"diagnostic.missing":    "Syntax error: missing %q",
		"diagnostic.unexpected": "Syntax error: unexpected %s",
		"diagnostic.endOfInput": "end of input",
		"diagnostic.unclosed":   "Syntax error: expected %q to close %q from line %d",
		// dap
		"dap.invalidLaunchArgs":  "invalid launch arguments: %s",
		"dap.missingProgram":     `launch configuration is missing "program"`,
		"dap.scriptNotFound":     "Script not found: %s",
		"dap.luaNotFound":        `Lua interpreter not found. Set "luaPath" in the launch configuration or the luaDevtools.luaPath setting.`,
		"dap.luaNotFoundAt":      "Lua interpreter not found: %s",
		"dap.luaStartFailed":     "Failed to start Lua: %s",
		"dap.notLaunched":        "no program launched",
		"dap.notPaused":          "the program is running; pause at a breakpoint first",
		"dap.unsupportedRequest": "unsupported request: %s",
		"dap.breakpointsQueued":  "Applied at the next pause",
		"dap.breakpointsPending": "Waiting for the next Lua debug hook",
		"dap.pauseUnavailable":   "Pause requires the native polling helper and debugging to be enabled",
	},
	"zh-cn": {
		"codelens.runTest":       "$(beaker) 运行测试",
		"codelens.debugTest":     "$(debug-alt) 调试测试",
		"codelens.run":           "$(play) 运行",
		"codelens.debug":         "$(debug-alt) 调试",
		"hover.definedOnLine":    "定义于第 %d 行",
		"hover.globalUndefined":  "本文件中未定义",
		"completion.keyword":     "关键字",
		"diagnostic.missing":     "语法错误：缺少 %q",
		"diagnostic.unexpected":  "语法错误：意外的 %s",
		"diagnostic.endOfInput":  "输入结束",
		"diagnostic.unclosed":    "语法错误：缺少 %q，以闭合 %q（起始于第 %d 行）",
		"dap.invalidLaunchArgs":  "launch 参数无效：%s",
		"dap.missingProgram":     `launch 配置缺少 "program"`,
		"dap.scriptNotFound":     "找不到脚本：%s",
		"dap.luaNotFound":        `找不到 Lua 解释器。请在 launch 配置的 "luaPath" 或设置 luaDevtools.luaPath 中指定。`,
		"dap.luaNotFoundAt":      "找不到 Lua 解释器：%s",
		"dap.luaStartFailed":     "启动 Lua 失败：%s",
		"dap.notLaunched":        "尚未启动程序",
		"dap.notPaused":          "程序正在运行，请先在断点处暂停",
		"dap.unsupportedRequest": "不支持的请求：%s",
		"dap.breakpointsQueued":  "将在下次暂停时生效",
		"dap.breakpointsPending": "等待下一个 Lua 调试 hook 生效",
		"dap.pauseUnavailable":   "主动暂停需要加载原生轮询模块并启用调试",
	},
}
