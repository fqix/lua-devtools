// lua-dap is a Debug Adapter Protocol server for Lua scripts: it speaks DAP on
// stdin/stdout and drives lua/debugger.lua.
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/fqix/lua-devtools/internal/dap"
)

func main() {
	debuggerScript := flag.String("debugger-script", "", "path to lua/debugger.lua (default: <exe>/../lua/debugger.lua)")
	luaPath := flag.String("lua", "", "Lua interpreter to use when the launch configuration does not set luaPath")
	logFile := flag.String("log", "", "append diagnostic logs to this file (stderr by default)")
	flag.Parse()

	// stdout is the protocol channel; logs must never go there.
	log.SetOutput(os.Stderr)
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("open log file: %v", err)
		}
		defer f.Close()
		log.SetOutput(f)
	}

	script := *debuggerScript
	if script == "" {
		exe, err := os.Executable()
		if err != nil {
			log.Fatalf("locate executable: %v", err)
		}
		script = filepath.Join(filepath.Dir(exe), "..", "lua", "debugger.lua")
	}
	if _, err := os.Stat(script); err != nil {
		log.Fatalf("debugger script not found: %s", script)
	}

	server := dap.NewServer(os.Stdin, os.Stdout, dap.Options{DebuggerScript: script, DefaultLuaPath: *luaPath})
	if err := server.Run(); err != nil {
		log.Fatalf("dap server: %v", err)
	}
}
