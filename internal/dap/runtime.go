package dap

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fqix/lua-devtools/internal/i18n"
)

// Runtime wraps a `lua debugger.lua <script>` process.
//
// Protocol: commands go to stdin; replies and events use a private session file.
// stdout and stderr carry only program output.
// With the optional native helper, Lua also reads commands from debug hooks.
// Otherwise running breakpoint changes are flushed on the next stop.
type Runtime struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	commands   chan []byte // one writer; bounded so a blocked native call cannot stall DAP
	connection net.Conn
	input      io.WriteCloser
	inputQueue chan inputChunk
	inputMu    sync.Mutex

	mu                 sync.Mutex
	nextID             int
	pending            map[int]chan reply
	paused             bool
	activeThread       int
	liveControl        bool
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

	ThreadID int    // stopped
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

	ThreadID    int                  `json:"threadId"`
	Event       string               `json:"event"`
	Path        string               `json:"path"` // resolvePath
	Reason      string               `json:"reason"`
	File        string               `json:"file"`
	Line        int                  `json:"line"`
	Text        string               `json:"text"`
	Category    string               `json:"category"`
	ExitCode    int                  `json:"exitCode"`
	Breakpoints []VerifiedBreakpoint `json:"breakpoints"`
}

type LaunchOptions struct {
	Interactive    bool
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
		commands:           make(chan []byte, 64),
		nextID:             1,
		pending:            map[int]chan reply{},
		pendingBreakpoints: map[string][]SourceBreakpoint{},
		Events:             make(chan Event, 64),
	}
}

// Start spawns the Lua process. debugger.lua blocks waiting for `run`, so the
// runtime is considered paused right away and queued breakpoints are flushed.
func (r *Runtime) Start(opts LaunchOptions) error {
	var listener *net.TCPListener
	var token string
	if opts.Interactive {
		var err error
		listener, err = net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return err
		}
		defer listener.Close()
		token = rand.Text()
	}
	args := append([]string{opts.DebuggerScript, opts.Program}, opts.Args...)
	cmd := exec.Command(opts.LuaPath, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = append(buildEnv(opts), "LUA_DEVTOOLS_CONNECT=", "LUA_DEVTOOLS_TOKEN=")
	if listener != nil {
		cmd.Env = append(cmd.Env, "LUA_DEVTOOLS_CONNECT="+listener.Addr().String(), "LUA_DEVTOOLS_TOKEN="+token)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	protocol, err := os.CreateTemp("", "lua-devtools-protocol-*")
	if err != nil {
		_ = stdin.Close()
		return err
	}
	cleanup := func() {
		_ = protocol.Close()
		_ = os.Remove(protocol.Name())
	}
	cmd.Env = append(cmd.Env, "LUA_DEVTOOLS_PROTOCOL="+protocol.Name())
	stdout := &outputStream{runtime: r, category: "stdout"}
	stderr := &outputStream{runtime: r, category: "stderr"}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		cleanup()
		_ = stdin.Close()
		if errors.Is(err, exec.ErrNotFound) || os.IsNotExist(err) {
			return errors.New(i18n.T(r.locale, "dap.luaNotFoundAt", opts.LuaPath))
		}
		return errors.New(i18n.T(r.locale, "dap.luaStartFailed", err.Error()))
	}
	r.cmd = cmd
	r.stdin = stdin
	var networkReader io.Reader
	if listener != nil {
		_ = listener.SetDeadline(time.Now().Add(5 * time.Second))
		connection, err := listener.AcceptTCP()
		if err == nil {
			_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
			reader := bufio.NewReader(connection)
			var authentication struct {
				Token string `json:"token"`
			}
			line, readErr := reader.ReadSlice('\n')
			if readErr != nil || json.Unmarshal(line, &authentication) != nil || authentication.Token != token {
				err = errors.New("invalid debugger connection token")
			} else {
				networkReader = reader
			}
			_ = connection.SetReadDeadline(time.Time{})
		}
		if err != nil {
			if connection != nil {
				_ = connection.Close()
			}
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			_ = stdin.Close()
			cleanup()
			return fmt.Errorf("interactive debugging requires LuaSocket in the selected environment: %w", err)
		}
		r.connection = connection
		r.stdin = connection
		r.input = stdin
	}

	r.mu.Lock()
	r.paused = true
	r.mu.Unlock()

	done := make(chan struct{})
	if opts.Interactive {
		r.inputQueue = make(chan inputChunk, 16)
		go func() {
			defer func() {
				r.inputMu.Lock()
				r.input = nil
				r.inputMu.Unlock()
			}()
			for {
				select {
				case <-done:
					return
				case chunk := <-r.inputQueue:
					if _, err := io.WriteString(stdin, chunk.text); err != nil {
						return
					}
					if chunk.eof {
						_ = stdin.Close()
						return
					}
				}
			}
		}()
	}
	control := r.stdin
	go func() {
		for {
			select {
			case <-done:
				return
			case line := <-r.commands:
				if _, err := control.Write(line); err != nil {
					r.Dispose()
					return
				}
			}
		}
	}()
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		stdout.flush()
		stderr.flush()
		close(done)
		if r.connection != nil {
			_ = r.connection.Close()
		}
	}()
	go func() {
		defer cleanup()
		if networkReader != nil {
			r.readProtocol(networkReader)
		} else {
			r.readProtocol(&protocolReader{file: protocol, done: done})
		}
		<-done
		code := 0
		if waitErr != nil {
			code = 1
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
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
		r.Events <- Event{Kind: "exited", ExitCode: code}
		r.Events <- Event{Kind: "exit", ExitCode: code}
	}()
	// Negotiate before launch completes; a missing helper is a supported fallback.
	ready := make(chan error, 1)
	var capabilities struct {
		LiveControl bool `json:"liveControl"`
	}
	go func() { ready <- r.call("capabilities", nil, &capabilities) }()
	select {
	case err := <-ready:
		if err != nil {
			r.Dispose()
			return err
		}
	case <-time.After(5 * time.Second):
		r.Dispose()
		return errors.New("Lua debugger startup timed out")
	}
	r.mu.Lock()
	r.liveControl = capabilities.LiveControl
	r.mu.Unlock()
	go r.flushPendingBreakpoints()
	return nil
}

// Dispose kills the process if it is still running.
func (r *Runtime) Dispose() {
	if r.connection != nil {
		_ = r.connection.Close()
	}
	if r.cmd != nil && r.cmd.Process != nil {
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
	err := r.queueCommand(line)
	r.mu.Unlock()
	if err != nil {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
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
	if id, ok := payload["threadId"].(int); ok && id > 0 && cmd != "continue" && cmd != "run" && cmd != "resumeThread" && id != r.activeThread {
		r.mu.Unlock()
		return fmt.Errorf("only the currently stopped coroutine can be stepped")
	}
	r.paused = false
	if payload["noDebug"] == true {
		r.liveControl = false
	}
	r.mu.Unlock()
	err := r.call(cmd, payload, nil)
	if err != nil {
		r.mu.Lock()
		r.paused = true
		r.mu.Unlock()
	}
	return err
}

func (r *Runtime) SetBreakpoints(file string, bps []SourceBreakpoint) ([]VerifiedBreakpoint, error) {
	r.mu.Lock()
	paused := r.paused && r.stdin != nil
	if !paused {
		live := r.liveControl && r.stdin != nil && !r.closed
		if !live {
			r.pendingBreakpoints[file] = bps
		}
		r.mu.Unlock()
		if live {
			if bps == nil {
				bps = []SourceBreakpoint{}
			}
			if err := r.notify(map[string]any{"cmd": "updateBreakpoints", "file": file, "breakpoints": bps}); err != nil {
				return nil, err
			}
		}
		out := make([]VerifiedBreakpoint, len(bps))
		for i, bp := range bps {
			key := "dap.breakpointsQueued"
			if live {
				key = "dap.breakpointsPending"
			}
			out[i] = VerifiedBreakpoint{Line: bp.Line, Verified: false, Message: i18n.T(r.locale, key)}
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

// notify queues control without waiting for a hook: a long C call must not block
// the DAP request loop from handling disconnect/terminate.
func (r *Runtime) notify(msg map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stdin == nil || r.closed {
		return errors.New("Lua process is not running")
	}
	line, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return r.queueCommand(line)
}

func (r *Runtime) queueCommand(line []byte) error {
	select {
	case r.commands <- append(line, '\n'):
		return nil
	default:
		return errors.New("Lua debugger command queue is full")
	}
}

func (r *Runtime) Pause() error {
	r.mu.Lock()
	paused, live := r.paused, r.liveControl
	r.mu.Unlock()
	if paused {
		return nil
	}
	if !live {
		return errors.New(i18n.T(r.locale, "dap.pauseUnavailable"))
	}
	return r.notify(map[string]any{"cmd": "pause"})
}

type ThreadInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (r *Runtime) Threads() ([]ThreadInfo, error) {
	var body struct {
		Threads []ThreadInfo `json:"threads"`
	}
	err := r.call("threads", nil, &body)
	return body.Threads, err
}

func (r *Runtime) Stack(threadIDs ...int) ([]StackFrameInfo, error) {
	var body struct {
		Frames []StackFrameInfo `json:"frames"`
	}
	payload := map[string]any{}
	if len(threadIDs) > 0 {
		payload["threadId"] = threadIDs[0]
	}
	err := r.call("stack", payload, &body)
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

func (r *Runtime) readProtocol(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var msg message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			r.Events <- Event{Kind: "output", Category: "stderr", Text: "Invalid debugger protocol: " + err.Error() + "\n"}
			r.Dispose()
			return
		}
		r.handleMessage(msg)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, net.ErrClosed) {
		r.Events <- Event{Kind: "output", Category: "stderr", Text: "Debugger protocol read failed: " + err.Error() + "\n"}
		r.Dispose()
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
	case "breakpointsChanged":
		r.Events <- Event{Kind: "breakpointsChanged", BreakpointFile: msg.File, Breakpoints: msg.Breakpoints}
	case "stopped":
		r.mu.Lock()
		r.activeThread = msg.ThreadID
		r.paused = true
		r.mu.Unlock()
		// Flushing sends commands and waits for replies that this very goroutine
		// reads, so it must run elsewhere. The stopped event follows the flush so
		// breakpoint updates reach the client first.
		go func() {
			r.flushPendingBreakpoints()
			r.Events <- Event{Kind: "stopped", ThreadID: msg.ThreadID, Reason: msg.Reason, File: msg.File, Line: msg.Line, Text: msg.Text}
		}()
	case "output":
		r.Events <- Event{Kind: "output", Category: msg.Category, Text: msg.Text}
	case "resolvePath":
		// Lua asks for the canonical form of a chunk path so breakpoint matching
		// uses the same symlink-resolved paths as setBreakpoints.
		line, _ := json.Marshal(map[string]string{"path": msg.Path, "resolvedPath": NormalizePath(msg.Path)})
		r.mu.Lock()
		if r.stdin != nil && !r.closed {
			if err := r.queueCommand(line); err != nil {
				r.Dispose()
			}
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
