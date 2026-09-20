//go:build integration

package dap

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func socketInterpreter(t *testing.T) string {
	t.Helper()
	lua := os.Getenv("LUA_TEST_BINARY")
	if lua == "" {
		lua = FindLuaInterpreter()
	}
	if lua == "" {
		t.Skip("Lua unavailable")
	}
	if output, err := exec.Command(lua, "-e", `require("socket")`).CombinedOutput(); err != nil {
		if os.Getenv("LUA_TEST_SOCKET_REQUIRED") == "1" {
			t.Fatalf("LuaSocket required: %s", output)
		}
		t.Skip("LuaSocket unavailable for selected interpreter")
	}
	return lua
}

func TestInteractiveInputIsolation(t *testing.T) {
	lua := socketInterpreter(t)
	dir := t.TempDir()
	program := filepath.Join(dir, "input.lua")
	source := `local line=io.read("*l")
assert(line=='{"cmd":"continue"}', line)
local rest=io.read("*a")
assert(rest=="中文",rest)
print("INPUT_OK")`
	if err := os.WriteFile(program, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	debugger, _ := filepath.Abs("../../vscode/lua/debugger.lua")
	r := NewRuntime()
	t.Cleanup(r.Dispose)
	if err := r.Start(LaunchOptions{LuaPath: lua, DebuggerScript: debugger, Program: program, Cwd: dir, Interactive: true}); err != nil {
		t.Fatal(err)
	}
	runtimeRequest(t, func() error { return r.Resume("run", map[string]any{"cwd": dir}) })
	if err := r.Input("{\"cmd\":\"continue\"}\n", false); err != nil {
		t.Fatal(err)
	}
	if err := r.Input("中文", true); err != nil {
		t.Fatal(err)
	}
	if err := r.Input("after EOF", false); err == nil {
		t.Fatal("input after EOF accepted")
	}
	var output strings.Builder
	for {
		select {
		case event := <-r.Events:
			if event.Kind == "output" {
				output.WriteString(event.Text)
			}
			if event.Kind == "exit" {
				if event.ExitCode != 0 || !strings.Contains(output.String(), "INPUT_OK") {
					t.Fatalf("%+v %s", event, output.String())
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("input blocked")
		}
	}
}

func TestAttachDetachPreservesHost(t *testing.T) {
	lua := socketInterpreter(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	dir := t.TempDir()
	program := filepath.Join(dir, "host.lua")
	marker := filepath.Join(dir, "survived.txt")
	bootstrap, _ := filepath.Abs("../../vscode/lua/lua-devtools.lua")
	source := fmt.Sprintf(`local originalCreate=coroutine.create
local oldHook=function() end
debug.sethook(oldHook,"l")
local session=dofile(%q).listen{port=%d,token="test-token"}
local value=41
local co=coroutine.create(function()
  value=value+1
end)
assert(coroutine.resume(co))
session.stop()
assert(coroutine.create==originalCreate)
assert(debug.gethook()==oldHook)
assert(io.open(%q,"w")):write(tostring(value)):close()
`, bootstrap, port, marker)
	if err := os.WriteFile(program, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	process := exec.Command(lua, program)
	process.Dir = dir
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if process.Process != nil {
			process.Process.Kill()
		}
	})
	r := NewRuntime()
	t.Cleanup(r.Dispose)
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = r.Attach(AttachArguments{Port: port, Token: "test-token"})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	runtimeRequest(t, func() error {
		_, err := r.SetBreakpoints(NormalizePath(program), []SourceBreakpoint{{Line: 7}})
		return err
	})
	runtimeRequest(t, func() error { return r.Resume("run", map[string]any{"cwd": dir}) })
	runtimeEvent(t, r, "stopped")
	runtimeRequest(t, func() error {
		v, err := r.Evaluate("value", 1)
		if err == nil && v.Result != "41" {
			t.Fatalf("value=%+v", v)
		}
		return err
	})
	r.Dispose()
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host did not survive detach")
	}
	value, err := os.ReadFile(marker)
	if err != nil || string(value) != "42" {
		t.Fatalf("marker %q %v", value, err)
	}
}

func TestResumeSuspendedCoroutine(t *testing.T) {
	r, program := startTestRuntime(t, `local co=coroutine.create(function()
  local value=1
  coroutine.yield(value)
  value=value+1
  _G.resumed=value
end)
assert(coroutine.resume(co))
local stop=1
assert(resumed==2)
`)
	runtimeRequest(t, func() error { _, err := r.SetBreakpoints(program, []SourceBreakpoint{{Line: 8}}); return err })
	runtimeRequest(t, func() error { return r.Resume("run", nil) })
	runtimeEvent(t, r, "stopped")
	runtimeRequest(t, func() error { return r.Resume("resumeThread", map[string]any{"threadId": 2}) })
	runtimeEvent(t, r, "stopped")
	runtimeRequest(t, func() error {
		v, err := r.Evaluate("resumed", 1)
		if err == nil && v.Result != "2" {
			t.Fatalf("%+v", v)
		}
		return err
	})
	runtimeRequest(t, func() error { return r.Resume("continue", nil) })
	if event := runtimeEvent(t, r, "exit"); event.ExitCode != 0 {
		t.Fatal(event)
	}
}

func TestCaughtCoroutineExceptionInspection(t *testing.T) {
	r, _ := startTestRuntime(t, `local co=coroutine.create(function()
 local secret=42
 error("caught coroutine failure")
end)
local ok,message=coroutine.resume(co)
assert(not ok and message:find("caught coroutine failure"))
`)
	runtimeRequest(t, func() error { return r.Resume("run", map[string]any{"breakOnCoroutineErrors": true}) })
	event := runtimeEvent(t, r, "stopped")
	if event.ThreadID != 2 || event.Reason != "exception" {
		t.Fatal(event)
	}
	runtimeRequest(t, func() error {
		frames, err := r.Stack(2)
		if err != nil {
			return err
		}
		if len(frames) == 0 {
			t.Fatal("missing failed coroutine stack")
		}
		value, err := r.Evaluate("secret", frames[0].ID)
		if err == nil && value.Result != "42" {
			t.Fatalf("%+v", value)
		}
		return err
	})
	runtimeRequest(t, func() error { return r.Resume("continue", nil) })
	if event := runtimeEvent(t, r, "exit"); event.ExitCode != 0 {
		t.Fatal(event)
	}
}

// A host can catch a session:run error and continue debugging later work.
func TestEmbeddedProtectedErrorRestoresHooks(t *testing.T) {
	lua := socketInterpreter(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	dir := t.TempDir()
	program := filepath.Join(dir, "host.lua")
	bootstrap, _ := filepath.Abs("../../vscode/lua/lua-devtools.lua")
	source := fmt.Sprintf(`local session=dofile(%q).listen{port=%d}
local ok=pcall(function() session:run(function()
  local secret=42
  error("protected error")
end) end)
assert(not ok)
local continued=42
continued=continued+1
session.stop()
assert(continued==43)
`, bootstrap, port)
	if err := os.WriteFile(program, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	process := exec.Command(lua, program)
	process.Dir = dir
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { process.Process.Kill() })
	r := NewRuntime()
	t.Cleanup(r.Dispose)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err = r.Attach(AttachArguments{Port: port}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	runtimeRequest(t, func() error {
		_, err := r.SetBreakpoints(NormalizePath(program), []SourceBreakpoint{{Line: 8}})
		return err
	})
	runtimeRequest(t, func() error { return r.Resume("run", map[string]any{"cwd": dir}) })
	event := runtimeEvent(t, r, "stopped")
	if event.Reason != "exception" {
		t.Fatalf("%+v", event)
	}
	runtimeRequest(t, func() error { return r.Resume("continue", nil) })
	event = runtimeEvent(t, r, "stopped")
	if event.Line != 8 {
		t.Fatalf("hook not restored: %+v", event)
	}
	runtimeRequest(t, func() error {
		v, err := r.Evaluate("continued", 1)
		if err == nil && v.Result != "42" {
			t.Fatalf("%+v", v)
		}
		return err
	})
	r.Dispose()
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host blocked")
	}
}
