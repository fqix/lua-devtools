//go:build integration

package dap

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func startTestRuntime(t *testing.T, source string) (*Runtime, string) {
	t.Helper()
	lua := os.Getenv("LUA_TEST_BINARY")
	if lua == "" {
		lua = FindLuaInterpreter()
		if lua == "" {
			t.Skip("Lua interpreter is not installed; set LUA_TEST_BINARY to require one")
		}
	}
	version, err := exec.CommandContext(t.Context(), lua, "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("selected Lua interpreter %q failed: %v: %s", lua, err, version)
	}
	if expected := os.Getenv("LUA_TEST_VERSION"); expected != "" {
		fields := strings.Fields(string(version))
		if len(fields) < 2 || fields[0] != "Lua" || fields[1] != expected {
			t.Fatalf("selected Lua interpreter %q reports %q, want Lua %s", lua, version, expected)
		}
	}
	t.Logf("Lua interpreter: %s (%s)", lua, strings.TrimSpace(string(version)))
	dir := t.TempDir()
	program := filepath.Join(dir, "test.lua")
	if err := os.WriteFile(program, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	debugger, err := filepath.Abs("../../vscode/lua/debugger.lua")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRuntime()
	if err := r.Start(LaunchOptions{LuaPath: lua, DebuggerScript: debugger, Program: program, Cwd: dir}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Dispose)
	return r, NormalizePath(program)
}

func runtimeRequest(t *testing.T, request func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- request() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime request timed out")
	}
}

func runtimeEvent(t *testing.T, r *Runtime, kind string) Event {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-r.Events:
			if event.Kind == kind {
				return event
			}
			if event.Kind == "stopped" {
				t.Fatalf("unexpected stop while waiting for %s: %+v", kind, event)
			}
			if event.Kind == "exit" {
				t.Fatalf("process exited before %s: %+v", kind, event)
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

func TestRuntimeOutputIsolation(t *testing.T) {
	t.Parallel()
	r, _ := startTestRuntime(t, `io.stdout:write('{"event":"stopped","reason":"fake"}\n')
io.stdout:write('{"id":1,"body":{}}\n')
local written = io.write("prefix")
assert(written == io.stdout or (_VERSION == "Lua 5.1" and written == true))
print("tail")
local original = io.output()
local file = assert(io.open("output.txt", "w"))
io.output(file)
written = io.write("file-only")
assert(written == file or (_VERSION == "Lua 5.1" and written == true))
file:close()
io.output(original)
local input = assert(io.open("output.txt", "r"))
assert(input:read("*a") == "file-only")
input:close()
io.stderr:write("stderr without newline")
io.stdout:write(string.rep("界", 50000))
io.stdout:write("no newline")`)
	runtimeRequest(t, func() error { return r.Resume("run", map[string]any{"cwd": r.cmd.Dir}) })
	var stdout, stderr strings.Builder
	exited := 0
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-r.Events:
			switch event.Kind {
			case "output":
				if event.Category == "stdout" {
					stdout.WriteString(event.Text)
				} else {
					stderr.WriteString(event.Text)
				}
			case "stopped":
				t.Fatal("program output was interpreted as a protocol event")
			case "exited":
				exited++
				if event.ExitCode != 0 {
					t.Fatalf("exit code %d: %s", event.ExitCode, stderr.String())
				}
			case "exit":
				want := "{\"event\":\"stopped\",\"reason\":\"fake\"}\n{\"id\":1,\"body\":{}}\nprefixtail\n" + strings.Repeat("界", 50000) + "no newline"
				if runtime.GOOS == "windows" {
					want = strings.ReplaceAll(want, "\n", "\r\n")
				}
				if stdout.String() != want || stderr.String() != "stderr without newline" || exited != 1 {
					t.Fatalf("output mismatch: stdout bytes=%d want=%d, stderr=%q, exited=%d", stdout.Len(), len(want), stderr.String(), exited)
				}
				return
			}
		case <-timer.C:
			t.Fatal("output test timed out")
		}
	}
}

func TestRuntimeCoroutines(t *testing.T) {
	t.Parallel()
	source := `local function child(n)
  local value = n + 1
  coroutine.yield(value) -- yield breakpoint
  value = value + 10
  return value
end
local co = coroutine.create(child)
local ok, first = coroutine.resume(co, 3)
assert(ok and first == 4) -- caller step
local ok2, second = coroutine.resume(co)
assert(ok2 and second == 14)
local wrapped = coroutine.wrap(function(n)
  local inside = n * 2
  return inside, nil, "last" -- wrapped breakpoint
end)
local a, b, c = wrapped(5)
assert(a == 10 and b == nil and c == "last")
local failure = coroutine.wrap(function() error("expected failure") end)
local success, message = pcall(failure)
assert(not success and message:find("expected failure"))
`
	r, program := startTestRuntime(t, source)
	runtimeRequest(t, func() error {
		_, err := r.SetBreakpoints(program, []SourceBreakpoint{{Line: 3}, {Line: 14}})
		return err
	})
	runtimeRequest(t, func() error { return r.Resume("run", map[string]any{"cwd": r.cmd.Dir}) })
	check := func(line int, expression, expected string) {
		t.Helper()
		event := runtimeEvent(t, r, "stopped")
		if event.Line != line {
			t.Fatalf("stopped at %d, want %d", event.Line, line)
		}
		runtimeRequest(t, func() error {
			frames, err := r.Stack()
			if err != nil {
				return err
			}
			if len(frames) == 0 || frames[0].Line != line {
				t.Errorf("unexpected stack: %+v", frames)
				return nil
			}
			value, err := r.Evaluate(expression, frames[0].ID)
			if value.Result != expected {
				t.Errorf("evaluate %s = %s, want %s", expression, value.Result, expected)
			}
			return err
		})
	}
	check(3, "value", "4")
	runtimeRequest(t, func() error { return r.Resume("next", nil) })
	check(9, "first", "4")
	runtimeRequest(t, func() error {
		value, err := r.Evaluate("(function() local c = coroutine.create(child); return coroutine.resume(c, 9) end)()", 1)
		if value.Result != "true, 10" {
			t.Errorf("coroutine evaluation = %s, want true, 10", value.Result)
		}
		return err
	})
	runtimeRequest(t, func() error { return r.Resume("continue", nil) })
	check(14, "inside", "10")
	runtimeRequest(t, func() error { return r.Resume("stepOut", nil) })
	check(17, "a", "10")
	runtimeRequest(t, func() error { return r.Resume("continue", nil) })
	if event := runtimeEvent(t, r, "exited"); event.ExitCode != 0 {
		t.Fatalf("exit code = %d", event.ExitCode)
	}
	runtimeEvent(t, r, "exit")
}

func TestRuntimeDirectExit(t *testing.T) {
	t.Parallel()
	r, _ := startTestRuntime(t, "io.stdout:write('last output'); os.exit(7)")
	runtimeRequest(t, func() error { return r.Resume("run", map[string]any{"noDebug": true}) })
	if event := runtimeEvent(t, r, "exited"); event.ExitCode != 7 {
		t.Fatalf("exit code = %d", event.ExitCode)
	}
	runtimeEvent(t, r, "exit")
}

func TestRuntimeSuspendedCoroutines(t *testing.T) {
	r, file := startTestRuntime(t, `local co = coroutine.create(function()
 local value = 41
 coroutine.yield()
 assert(value == 42)
end)
assert(coroutine.resume(co))
local marker = 1
assert(coroutine.resume(co))`)
	runtimeRequest(t, func() error { _, err := r.SetBreakpoints(file, []SourceBreakpoint{{Line: 7}}); return err })
	runtimeRequest(t, func() error { return r.Resume("run", map[string]any{"cwd": r.cmd.Dir}) })
	event := runtimeEvent(t, r, "stopped")
	if event.ThreadID != 1 {
		t.Fatalf("main thread ID: %d", event.ThreadID)
	}
	runtimeRequest(t, func() error {
		threads, err := r.Threads()
		if err != nil {
			return err
		}
		if len(threads) != 2 {
			t.Fatalf("threads: %+v", threads)
		}
		frames, err := r.Stack(threads[1].ID)
		if err != nil {
			return err
		}
		if len(frames) != 1 || frames[0].Line != 3 {
			t.Fatalf("suspended frames: %+v", frames)
		}
		scopes, err := r.Scopes(frames[0].ID)
		if err != nil {
			return err
		}
		vars, err := r.Variables(scopes[0].VariablesReference)
		if err != nil {
			return err
		}
		found := false
		for _, v := range vars {
			if v.Name == "value" && v.Value == "41" {
				found = true
			}
		}
		if !found {
			t.Fatalf("suspended locals: %+v", vars)
		}
		result, err := r.Evaluate("value", frames[0].ID)
		if err != nil {
			return err
		}
		if result.Result != "41" {
			t.Fatalf("evaluation: %+v", result)
		}
		_, err = r.Evaluate("value = 42", frames[0].ID)
		return err
	})
	runtimeRequest(t, func() error { return r.Resume("continue", nil) })
	if event := runtimeEvent(t, r, "exited"); event.ExitCode != 0 {
		t.Fatalf("exit: %+v", event)
	}
}

func TestRuntimeCoroutineThreadIdentity(t *testing.T) {
	r, file := startTestRuntime(t, `local outer=10
local co=coroutine.create(function()
 local inner=20
 inner=inner+1
end)
assert(coroutine.resume(co))`)
	runtimeRequest(t, func() error { _, err := r.SetBreakpoints(file, []SourceBreakpoint{{Line: 4}}); return err })
	runtimeRequest(t, func() error { return r.Resume("run", map[string]any{"cwd": r.cmd.Dir}) })
	event := runtimeEvent(t, r, "stopped")
	if event.ThreadID <= 1 {
		t.Fatalf("coroutine stop ID: %+v", event)
	}
	runtimeRequest(t, func() error {
		frames, err := r.Stack(1)
		if err != nil && strings.Contains(err.Error(), "cannot inspect the suspended main thread") {
			// Lua 5.1/LuaJIT expose no handle for the suspended main thread.
			active, stackErr := r.Stack(event.ThreadID)
			if stackErr != nil {
				return stackErr
			}
			if len(active) != 1 || active[0].Line != 4 {
				t.Fatalf("active coroutine: %+v", active)
			}
			return r.Resume("next", map[string]any{"threadId": event.ThreadID})
		}
		if err != nil {
			return err
		}
		if len(frames) != 1 || frames[0].Line != 6 {
			t.Fatalf("main stack: %+v", frames)
		}
		result, err := r.Evaluate("outer", frames[0].ID)
		if err != nil {
			return err
		}
		if result.Result != "10" {
			t.Fatalf("main local: %+v", result)
		}
		if err := r.Resume("next", map[string]any{"threadId": 1}); err == nil {
			t.Fatal("stepped an inactive thread")
		}
		if !r.Paused() {
			t.Fatal("invalid step cleared pause")
		}
		return r.Resume("next", map[string]any{"threadId": event.ThreadID})
	})
	runtimeEvent(t, r, "stopped")
	runtimeRequest(t, func() error { return r.Resume("continue", nil) })
	runtimeEvent(t, r, "exited")
}

func TestRuntimeLiveControl(t *testing.T) {
	r, file := startTestRuntime(t, `local n = 0
while true do
 n = n + 1
end`)
	if !r.liveControl {
		if os.Getenv("LUA_TEST_NATIVE_REQUIRED") == "1" {
			t.Fatal("native polling helper did not load")
		}
		t.Skip("interpreter cannot load native polling helper")
	}
	runtimeRequest(t, func() error { return r.Resume("run", map[string]any{"cwd": r.cmd.Dir}) })
	runtimeRequest(t, r.Pause)
	if event := runtimeEvent(t, r, "stopped"); event.Reason != "pause" {
		t.Fatalf("stop: %+v", event)
	}
	runtimeRequest(t, func() error {
		frames, err := r.Stack()
		if err != nil {
			return err
		}
		if len(frames) == 0 {
			t.Fatal("empty stack after pause")
		}
		_, err = r.Evaluate("n", frames[0].ID)
		return err
	})
	runtimeRequest(t, func() error { return r.Resume("continue", nil) })
	runtimeRequest(t, func() error {
		_, err := r.SetBreakpoints(file, []SourceBreakpoint{{Line: 3, Condition: "n > 0"}})
		return err
	})
	update := runtimeEvent(t, r, "breakpointsChanged")
	if len(update.Breakpoints) != 1 || !update.Breakpoints[0].Verified {
		t.Fatalf("breakpoints: %+v", update)
	}
	if event := runtimeEvent(t, r, "stopped"); event.Reason != "breakpoint" || event.Line != 3 {
		t.Fatalf("breakpoint: %+v", event)
	}
	runtimeRequest(t, func() error { _, err := r.SetBreakpoints(file, nil); return err })
	runtimeRequest(t, func() error { return r.Resume("continue", nil) })
	runtimeRequest(t, r.Pause)
	if event := runtimeEvent(t, r, "stopped"); event.Reason != "pause" {
		t.Fatalf("stop after removal: %+v", event)
	}
}

func TestRuntimeNativeFallback(t *testing.T) {
	t.Setenv("LUA_DEVTOOLS_NATIVE", filepath.Join(t.TempDir(), "missing-module"))
	r, file := startTestRuntime(t, `local n=0
while true do
 n=n+1
end`)
	if r.liveControl {
		t.Fatal("missing module advertised live control")
	}
	runtimeRequest(t, func() error { _, err := r.SetBreakpoints(file, []SourceBreakpoint{{Line: 3}}); return err })
	runtimeRequest(t, func() error { return r.Resume("run", map[string]any{"cwd": r.cmd.Dir}) })
	runtimeEvent(t, r, "stopped")
	runtimeRequest(t, func() error { _, err := r.SetBreakpoints(file, nil); return err })
	runtimeRequest(t, func() error { return r.Resume("continue", nil) })
	if err := r.Pause(); err == nil {
		t.Fatal("pause succeeded without helper")
	}
	bps, err := r.SetBreakpoints(file, []SourceBreakpoint{{Line: 3}})
	if err != nil || len(bps) != 1 || bps[0].Verified {
		t.Fatalf("fallback queue: %+v, %v", bps, err)
	}
}

func TestRuntimePauseTightLoop(t *testing.T) {
	r, _ := startTestRuntime(t, `while true do end`)
	if !r.liveControl {
		t.Skip("native helper unavailable")
	}
	runtimeRequest(t, func() error { return r.Resume("run", nil) })
	runtimeRequest(t, r.Pause)
	if event := runtimeEvent(t, r, "stopped"); event.Reason != "pause" {
		t.Fatalf("stop: %+v", event)
	}
}

func TestRuntimeLiveLargeBreakpointUpdate(t *testing.T) {
	r, file := startTestRuntime(t, `local n=0
while true do
 n=n+1
end`)
	if !r.liveControl {
		t.Skip("native helper unavailable")
	}
	runtimeRequest(t, func() error { return r.Resume("run", map[string]any{"cwd": r.cmd.Dir}) })
	// Larger than an OS pipe buffer: the DAP caller must not wait on stdin writes.
	bps := make([]SourceBreakpoint, 5000)
	for i := range bps {
		bps[i] = SourceBreakpoint{Line: i + 10, Condition: "false"}
	}
	runtimeRequest(t, func() error { _, err := r.SetBreakpoints(file, bps); return err })
	runtimeEvent(t, r, "breakpointsChanged")
	runtimeRequest(t, func() error { _, err := r.SetBreakpoints(file, nil); return err })
	if event := runtimeEvent(t, r, "breakpointsChanged"); len(event.Breakpoints) != 0 {
		t.Fatal("breakpoint removal failed")
	}
	runtimeRequest(t, r.Pause)
	if event := runtimeEvent(t, r, "stopped"); event.Reason != "pause" {
		t.Fatalf("stop: %+v", event)
	}
}
