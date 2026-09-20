package dap

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fqix/lua-devtools/internal/i18n"
)

// Runtime wraps a `lua debugger.lua <script>` process.
//
// Protocol: commands go to stdin, replies and events come from stdout, one JSON per line.
// Lua only reads stdin while paused, so breakpoint changes made while running are
// queued and flushed on the next `stopped` event.
type Runtime struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	mu                 sync.Mutex
	nextID             int
	pending            map[int]chan reply
	paused             bool
	pendingBreakpoints map[string][]SourceBreakpoint
	closed             bool
	locale             string

	// Events delivers stopped/output/exited/exit notifications in order.
	Events chan Event
}

type reply struct {
	body json.RawMessage
	err  error
}

// SourceBreakpoint is what the Lua side accepts for one breakpoint.
type SourceBreakpoint struct {
	Line      int    `json:"line"`
	Condition string `json:"condition,omitempty"`
}

// VerifiedBreakpoint is what the Lua side (or the queue) reports back.
type VerifiedBreakpoint struct {
	Line     int    `json:"line"`
	Verified bool   `json:"verified"`
	Message  string `json:"message,omitempty"`
}

type StackFrameInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	File string `json:"file"`
	Line int    `json:"line"`
}

type ScopeInfo struct {
	Name               string `json:"name"`
	VariablesReference int    `json:"variablesReference"`
}

type VariableInfo struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	Type               string `json:"type"`
	VariablesReference int    `json:"variablesReference"`
}

type EvaluateInfo struct {
	Result             string `json:"result"`
	Type               string `json:"type"`
	VariablesReference int    `json:"variablesReference"`
}

// Event is one notification from the Lua process.
type Event struct {
	Kind string // "stopped" | "output" | "exited" | "exit" | "breakpointsChanged"

	Reason   string // stopped
	File     string // stopped
	Line     int    // stopped
	Text     string // stopped (exception message) / output text
	Category string // output
	ExitCode int    // exited / exit

	BreakpointFile string               // breakpointsChanged
	Breakpoints    []VerifiedBreakpoint // breakpointsChanged
}

// message is the wire format coming from debugger.lua.
type message struct {
	ID    *int            `json:"id"`
	Body  json.RawMessage `json:"body"`
	Error *string         `json:"error"`

	Event    string `json:"event"`
	Path     string `json:"path"` // resolvePath
	Reason   string `json:"reason"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Text     string `json:"text"`
	Category string `json:"category"`
	ExitCode int    `json:"exitCode"`
}

type LaunchOptions struct {
	LuaPath        string
	DebuggerScript string
	Program        string
	Args           []string
	Cwd            string
	PackagePath    []string          // extra LUA_PATH entries
	PackageCPath   []string          // extra LUA_CPATH entries
	Env            map[string]string // extra environment variables
}

func NewRuntime() *Runtime {
	return &Runtime{
		nextID:             1,
		pending:            map[int]chan reply{},
		pendingBreakpoints: map[string][]SourceBreakpoint{},
		Events:             make(chan Event, 64),
	}
}

// Start spawns the Lua process. debugger.lua blocks waiting for `run`, so the
// runtime is considered paused right away and queued breakpoints are flushed.
func (r *Runtime) Start(opts LaunchOptions) error {
	args := append([]string{opts.DebuggerScript, opts.Program}, opts.Args...)
	cmd := exec.Command(opts.LuaPath, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = buildEnv(opts)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || os.IsNotExist(err) {
			return errors.New(i18n.T(r.locale, "dap.luaNotFoundAt", opts.LuaPath))
		}
		return errors.New(i18n.T(r.locale, "dap.luaStartFailed", err.Error()))
	}
	r.cmd = cmd
	r.stdin = stdin

	r.mu.Lock()
	r.paused = true
	r.mu.Unlock()

	go r.readStdout(stdout)
	go r.readStderr(stderr)
	go r.flushPendingBreakpoints()
	return nil
}

// Dispose kills the process if it is still running.
func (r *Runtime) Dispose() {
	if r.cmd != nil && r.cmd.Process != nil && r.cmd.ProcessState == nil {
		_ = r.cmd.Process.Kill()
	}
}

// send writes one command and waits for its reply.
func (r *Runtime) send(cmd string, payload map[string]any) (json.RawMessage, error) {
	r.mu.Lock()
	if r.stdin == nil || r.closed {
		r.mu.Unlock()
		return nil, errors.New("Lua process is not running")
	}
	id := r.nextID
	r.nextID++
	ch := make(chan reply, 1)
	r.pending[id] = ch

	msg := map[string]any{"id": id, "cmd": cmd}
	for k, v := range payload {
		msg[k] = v
	}
	line, _ := json.Marshal(msg)
	_, err := r.stdin.Write(append(line, '\n'))
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}

	rep := <-ch
	return rep.body, rep.err
}

func (r *Runtime) call(cmd string, payload map[string]any, out any) error {
	body, err := r.send(cmd, payload)
	if err != nil {
		return err
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}

// Paused reports whether Lua is waiting for commands (breakpoint, step, or before `run`).
func (r *Runtime) Paused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused && r.stdin != nil && !r.closed
}

// Resume sends run/continue/next/stepIn/stepOut. The runtime is marked as running
// before the reply arrives so later breakpoint changes get queued.
func (r *Runtime) Resume(cmd string, payload map[string]any) error {
	r.mu.Lock()
	r.paused = false
	r.mu.Unlock()
	return r.call(cmd, payload, nil)
}

func (r *Runtime) SetBreakpoints(file string, bps []SourceBreakpoint) ([]VerifiedBreakpoint, error) {
	r.mu.Lock()
	paused := r.paused && r.stdin != nil
	if !paused {
		r.pendingBreakpoints[file] = bps
		r.mu.Unlock()
		out := make([]VerifiedBreakpoint, len(bps))
		for i, bp := range bps {
			out[i] = VerifiedBreakpoint{Line: bp.Line, Verified: false, Message: i18n.T(r.locale, "dap.breakpointsQueued")}
		}
		return out, nil
	}
	r.mu.Unlock()

	var body struct {
		Breakpoints []VerifiedBreakpoint `json:"breakpoints"`
	}
	if bps == nil {
		bps = []SourceBreakpoint{}
	}
	err := r.call("setBreakpoints", map[string]any{"file": file, "breakpoints": bps}, &body)
	return body.Breakpoints, err
}

func (r *Runtime) Stack() ([]StackFrameInfo, error) {
	var body struct {
		Frames []StackFrameInfo `json:"frames"`
	}
	err := r.call("stack", nil, &body)
	return body.Frames, err
}

func (r *Runtime) Scopes(frameID int) ([]ScopeInfo, error) {
	var body struct {
		Scopes []ScopeInfo `json:"scopes"`
	}
	err := r.call("scopes", map[string]any{"frameId": frameID}, &body)
	return body.Scopes, err
}

func (r *Runtime) Variables(ref int) ([]VariableInfo, error) {
	var body struct {
		Variables []VariableInfo `json:"variables"`
	}
	err := r.call("variables", map[string]any{"ref": ref}, &body)
	return body.Variables, err
}

func (r *Runtime) Evaluate(expression string, frameID int) (EvaluateInfo, error) {
	var body EvaluateInfo
	payload := map[string]any{"expression": expression}
	if frameID > 0 {
		payload["frameId"] = frameID
	}
	err := r.call("evaluate", payload, &body)
	return body, err
}

func (r *Runtime) readStdout(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg message
		if err := json.Unmarshal(line, &msg); err != nil {
			// The script wrote to stdout directly, bypassing print; show it as plain output.
			r.Events <- Event{Kind: "output", Category: "stdout", Text: string(line) + "\n"}
			continue
		}
		r.handleMessage(msg)
	}
	// stdout closed: the process is gone.
	err := r.cmd.Wait()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
	}
	r.mu.Lock()
	r.closed = true
	for id, ch := range r.pending {
		ch <- reply{err: errors.New("Lua process exited")}
		delete(r.pending, id)
	}
	r.mu.Unlock()
	r.Events <- Event{Kind: "exit", ExitCode: code}
}

func (r *Runtime) readStderr(stderr io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := stderr.Read(buf)
		if n > 0 {
			r.Events <- Event{Kind: "output", Category: "stderr", Text: string(buf[:n])}
		}
		if err != nil {
			return
		}
	}
}

func (r *Runtime) handleMessage(msg message) {
	if msg.ID != nil {
		r.mu.Lock()
		ch, ok := r.pending[*msg.ID]
		delete(r.pending, *msg.ID)
		r.mu.Unlock()
		if !ok {
			return
		}
		if msg.Error != nil {
			ch <- reply{err: errors.New(*msg.Error)}
		} else {
			ch <- reply{body: msg.Body}
		}
		return
	}
	switch msg.Event {
	case "stopped":
		r.mu.Lock()
		r.paused = true
		r.mu.Unlock()
		// Flushing sends commands and waits for replies that this very goroutine
		// reads, so it must run elsewhere. The stopped event follows the flush so
		// breakpoint updates reach the client first.
		go func() {
			r.flushPendingBreakpoints()
			r.Events <- Event{Kind: "stopped", Reason: msg.Reason, File: msg.File, Line: msg.Line, Text: msg.Text}
		}()
	case "output":
		r.Events <- Event{Kind: "output", Category: msg.Category, Text: msg.Text}
	case "exited":
		r.Events <- Event{Kind: "exited", ExitCode: msg.ExitCode}
	case "resolvePath":
		// Lua asks for the canonical form of a chunk path so breakpoint matching
		// uses the same symlink-resolved paths as setBreakpoints.
		line, _ := json.Marshal(map[string]string{"path": msg.Path, "resolvedPath": NormalizePath(msg.Path)})
		r.mu.Lock()
		if r.stdin != nil && !r.closed {
			_, _ = r.stdin.Write(append(line, '\n'))
		}
		r.mu.Unlock()
	}
}

func (r *Runtime) flushPendingBreakpoints() {
	r.mu.Lock()
	queued := r.pendingBreakpoints
	r.pendingBreakpoints = map[string][]SourceBreakpoint{}
	r.mu.Unlock()
	for file, bps := range queued {
		verified, err := r.SetBreakpoints(file, bps)
		if err != nil {
			r.Events <- Event{Kind: "output", Category: "stderr", Text: fmt.Sprintf("Failed to set breakpoints in %s: %v\n", file, err)}
			continue
		}
		r.Events <- Event{Kind: "breakpointsChanged", BreakpointFile: file, Breakpoints: verified}
	}
}

// buildEnv extends the current environment with the launch configuration's
// variables and module search paths. A trailing ";;" makes Lua append its
// built-in default path, so project entries take precedence without replacing it.
func buildEnv(opts LaunchOptions) []string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for k, v := range opts.Env {
		env[k] = v
	}
	if len(opts.PackagePath) > 0 {
		env["LUA_PATH"] = strings.Join(opts.PackagePath, ";") + ";;"
	}
	if len(opts.PackageCPath) > 0 {
		env["LUA_CPATH"] = strings.Join(opts.PackageCPath, ";") + ";;"
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// FindLuaInterpreter looks for lua5.4 on PATH, then the Homebrew keg-only paths, then plain lua.
func FindLuaInterpreter() string {
	if p, err := exec.LookPath("lua5.4"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/opt/lua@5.4/bin/lua5.4", "/usr/local/opt/lua@5.4/bin/lua5.4"} {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("lua"); err == nil {
		return p
	}
	return ""
}

// NormalizePath resolves symlinks and makes the path absolute so it matches
// the `source` string Lua reports for the loaded chunk.
func NormalizePath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}
