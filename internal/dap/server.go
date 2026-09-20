// Package dap implements a Debug Adapter Protocol server for Lua scripts.
//
// The server speaks DAP over a reader/writer pair (stdio in practice) and drives
// lua/debugger.lua through the Runtime's line protocol. VS Code's request order is
// initialize -> launch -> setBreakpoints... -> configurationDone: `launch` only spawns
// the Lua process (debugger.lua then blocks waiting) and `configurationDone` sends `run`.
package dap

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/go-dap"

	"github.com/fqix/lua-devtools/internal/i18n"
)

const threadID = 1

// LaunchArguments mirrors the `launch` configuration in package.json.
type LaunchArguments struct {
	Program     string   `json:"program"`
	Args        []string `json:"args"`
	Cwd         string   `json:"cwd"`
	LuaPath     string   `json:"luaPath"`
	StopOnEntry bool     `json:"stopOnEntry"`
	NoDebug     bool     `json:"noDebug"` // set by the client for "Run Without Debugging"
	// Extra module search paths, exported as LUA_PATH / LUA_CPATH with the
	// interpreter defaults appended (the ";;" convention).
	PackagePath  []string          `json:"packagePath"`
	PackageCPath []string          `json:"packageCPath"`
	Env          map[string]string `json:"env"`
}

type Server struct {
	reader *bufio.Reader
	writer io.Writer
	wmu    sync.Mutex
	seq    int

	debuggerScript string
	defaultLuaPath string

	rt          *Runtime
	stopOnEntry bool
	noDebug     bool
	cwd         string
	locale      string // normalized client locale from initialize

	// Breakpoint ids per file, needed to update `verified` through breakpoint events.
	// Guarded by bpMu: both the request loop and the event pump assign ids.
	bpMu        sync.Mutex
	bpRequestMu sync.Mutex // send setBreakpoints response before asynchronous verification
	bpIDs       map[string]map[int]int
	nextBpID    int
}

type Options struct {
	DebuggerScript string
	DefaultLuaPath string
}

func NewServer(r io.Reader, w io.Writer, opts Options) *Server {
	return &Server{
		reader:         bufio.NewReader(r),
		writer:         w,
		debuggerScript: opts.DebuggerScript,
		defaultLuaPath: opts.DefaultLuaPath,
		locale:         "en",
		bpIDs:          map[string]map[int]int{},
		nextBpID:       1,
	}
}

// Run reads and handles requests until the client disconnects.
func (s *Server) Run() error {
	defer func() {
		if s.rt != nil {
			s.rt.Dispose()
		}
	}()
	codec := dap.NewCodec()
	if err := codec.RegisterRequest("lua/snapshot", func() dap.Message { return &snapshotRequest{} }, func() dap.Message { return &snapshotResponse{} }); err != nil {
		return err
	}
	for {
		content, err := dap.ReadBaseMessage(s.reader)
		var msg dap.Message
		if err == nil {
			msg, err = codec.DecodeMessage(content)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			var decodeErr *dap.DecodeProtocolMessageFieldError
			if errors.As(err, &decodeErr) {
				log.Printf("ignoring undecodable message: %v", err)
				continue
			}
			return err
		}
		if s.handle(msg) {
			return nil
		}
	}
}

// handle dispatches one message and reports whether the session is over.
func (s *Server) handle(msg dap.Message) bool {
	switch req := msg.(type) {
	case *dap.InitializeRequest:
		s.onInitialize(req)
	case *dap.LaunchRequest:
		s.onLaunch(req)
	case *dap.SetBreakpointsRequest:
		s.onSetBreakpoints(req)
	case *dap.SetExceptionBreakpointsRequest:
		s.send(&dap.SetExceptionBreakpointsResponse{Response: s.newResponse(req.Request)})
	case *dap.ConfigurationDoneRequest:
		s.onConfigurationDone(req)
	case *dap.ThreadsRequest:
		s.onThreads(req)
	case *dap.StackTraceRequest:
		s.onStackTrace(req)
	case *dap.ScopesRequest:
		s.onScopes(req)
	case *dap.VariablesRequest:
		s.onVariables(req)
	case *snapshotRequest:
		s.onSnapshot(req)
	case *dap.EvaluateRequest:
		s.onEvaluate(req)
	case *dap.ContinueRequest:
		s.onResume(req.Request, req.Arguments.ThreadId, "continue", func() dap.Message {
			return &dap.ContinueResponse{Response: s.newResponse(req.Request), Body: dap.ContinueResponseBody{AllThreadsContinued: true}}
		})
	case *dap.NextRequest:
		s.onResume(req.Request, req.Arguments.ThreadId, "next", func() dap.Message { return &dap.NextResponse{Response: s.newResponse(req.Request)} })
	case *dap.StepInRequest:
		s.onResume(req.Request, req.Arguments.ThreadId, "stepIn", func() dap.Message { return &dap.StepInResponse{Response: s.newResponse(req.Request)} })
	case *dap.StepOutRequest:
		s.onResume(req.Request, req.Arguments.ThreadId, "stepOut", func() dap.Message { return &dap.StepOutResponse{Response: s.newResponse(req.Request)} })
	case *dap.PauseRequest:
		if s.rt == nil {
			s.sendError(&req.Request, 1008, "Lua process is not running")
		} else if err := s.rt.Pause(); err != nil {
			s.sendError(&req.Request, 1008, err.Error())
		} else {
			s.send(&dap.PauseResponse{Response: s.newResponse(req.Request)})
		}
	case *dap.TerminateRequest:
		if s.rt != nil {
			s.rt.Dispose()
		}
		s.send(&dap.TerminateResponse{Response: s.newResponse(req.Request)})
	case *dap.DisconnectRequest:
		if s.rt != nil {
			s.rt.Dispose()
		}
		s.send(&dap.DisconnectResponse{Response: s.newResponse(req.Request)})
		return true
	default:
		if r, ok := msg.(dap.RequestMessage); ok {
			s.sendError(r.GetRequest(), 1000, s.t("dap.unsupportedRequest", r.GetRequest().Command))
		}
	}
	return false
}

func (s *Server) onInitialize(req *dap.InitializeRequest) {
	s.locale = i18n.Normalize(req.Arguments.Locale)
	s.send(&dap.InitializeResponse{
		Response: s.newResponse(req.Request),
		Body: dap.Capabilities{
			SupportsConfigurationDoneRequest: true,
			SupportsConditionalBreakpoints:   true,
			SupportsEvaluateForHovers:        true,
		},
	})
	// Tell the client it may start sending breakpoint configuration.
	s.send(&dap.InitializedEvent{Event: s.newEvent("initialized")})
}

func (s *Server) onLaunch(req *dap.LaunchRequest) {
	var args LaunchArguments
	if err := json.Unmarshal(req.Arguments, &args); err != nil {
		s.sendError(&req.Request, 1001, s.t("dap.invalidLaunchArgs", err.Error()))
		return
	}
	if args.Program == "" {
		s.sendError(&req.Request, 1001, s.t("dap.missingProgram"))
		return
	}
	program := NormalizePath(args.Program)
	if _, err := os.Stat(program); err != nil {
		s.sendError(&req.Request, 1001, s.t("dap.scriptNotFound", args.Program))
		return
	}
	luaPath := args.LuaPath
	if luaPath == "" {
		luaPath = s.defaultLuaPath
	}
	if luaPath == "" {
		luaPath = FindLuaInterpreter()
	}
	if luaPath == "" {
		s.sendError(&req.Request, 1001, s.t("dap.luaNotFound"))
		return
	}
	cwd := args.Cwd
	if cwd == "" {
		cwd = filepath.Dir(program)
	}
	cwd = NormalizePath(cwd)
	s.cwd = cwd

	// setBreakpoints may already have created the runtime to queue breakpoints.
	if s.rt == nil {
		s.rt = NewRuntime()
	}
	s.rt.locale = s.locale
	if err := s.rt.Start(LaunchOptions{
		LuaPath:        luaPath,
		DebuggerScript: s.debuggerScript,
		Program:        program,
		Args:           args.Args,
		Cwd:            cwd,
		PackagePath:    args.PackagePath,
		PackageCPath:   args.PackageCPath,
		Env:            args.Env,
	}); err != nil {
		s.sendError(&req.Request, 1001, err.Error())
		return
	}
	s.stopOnEntry = args.StopOnEntry
	s.noDebug = args.NoDebug
	go s.pumpEvents(s.rt)
	s.send(&dap.LaunchResponse{Response: s.newResponse(req.Request)})
}

// pumpEvents translates runtime notifications into DAP events.
func (s *Server) pumpEvents(rt *Runtime) {
	for ev := range rt.Events {
		switch ev.Kind {
		case "stopped":
			s.send(&dap.StoppedEvent{
				Event: s.newEvent("stopped"),
				Body:  dap.StoppedEventBody{Reason: ev.Reason, ThreadId: ev.ThreadID, Text: ev.Text, AllThreadsStopped: true},
			})
		case "output":
			s.send(&dap.OutputEvent{
				Event: s.newEvent("output"),
				Body:  dap.OutputEventBody{Category: ev.Category, Output: ev.Text},
			})
		case "exited":
			s.send(&dap.ExitedEvent{Event: s.newEvent("exited"), Body: dap.ExitedEventBody{ExitCode: ev.ExitCode}})
		case "exit":
			s.send(&dap.TerminatedEvent{Event: s.newEvent("terminated")})
			return
		case "breakpointsChanged":
			s.bpRequestMu.Lock()
			for _, bp := range s.toDapBreakpoints(ev.BreakpointFile, ev.Breakpoints) {
				s.send(&dap.BreakpointEvent{
					Event: s.newEvent("breakpoint"),
					Body:  dap.BreakpointEventBody{Reason: "changed", Breakpoint: bp},
				})
			}
			s.bpRequestMu.Unlock()
		}
	}
}

func (s *Server) onConfigurationDone(req *dap.ConfigurationDoneRequest) {
	if s.rt == nil {
		s.sendError(&req.Request, 1002, s.t("dap.notLaunched"))
		return
	}
	if err := s.rt.Resume("run", map[string]any{"stopOnEntry": s.stopOnEntry && !s.noDebug, "noDebug": s.noDebug, "cwd": s.cwd}); err != nil {
		s.sendError(&req.Request, 1002, err.Error())
		return
	}
	s.send(&dap.ConfigurationDoneResponse{Response: s.newResponse(req.Request)})
}

func (s *Server) onSetBreakpoints(req *dap.SetBreakpointsRequest) {
	s.bpRequestMu.Lock()
	defer s.bpRequestMu.Unlock()
	file := NormalizePath(req.Arguments.Source.Path)
	requested := make([]SourceBreakpoint, 0, len(req.Arguments.Breakpoints))
	for _, bp := range req.Arguments.Breakpoints {
		requested = append(requested, SourceBreakpoint{Line: bp.Line, Condition: bp.Condition})
	}
	if s.rt == nil {
		// Before launch: remember them in a runtime created on demand.
		s.rt = NewRuntime()
	}
	verified, err := s.rt.SetBreakpoints(file, requested)
	if err != nil {
		s.sendError(&req.Request, 1003, err.Error())
		return
	}
	s.send(&dap.SetBreakpointsResponse{
		Response: s.newResponse(req.Request),
		Body:     dap.SetBreakpointsResponseBody{Breakpoints: s.toDapBreakpoints(file, verified)},
	})
}

// requirePaused answers requests that need a paused debuggee with an error
// instead of inspecting a stack while the program is running.
func (s *Server) requirePaused(req *dap.Request, code int) bool {
	if s.rt == nil {
		s.sendError(req, code, s.t("dap.notLaunched"))
		return false
	}
	if !s.rt.Paused() {
		s.sendError(req, code, s.t("dap.notPaused"))
		return false
	}
	return true
}

func (s *Server) onThreads(req *dap.ThreadsRequest) {
	out := []dap.Thread{{Id: threadID, Name: "main"}}
	if s.rt != nil && s.rt.Paused() {
		threads, err := s.rt.Threads()
		if err != nil {
			s.sendError(&req.Request, 1004, err.Error())
			return
		}
		out = []dap.Thread{}
		for _, thread := range threads {
			out = append(out, dap.Thread{Id: thread.ID, Name: thread.Name})
		}
	}
	s.send(&dap.ThreadsResponse{Response: s.newResponse(req.Request), Body: dap.ThreadsResponseBody{Threads: out}})
}

func (s *Server) onStackTrace(req *dap.StackTraceRequest) {
	if !s.requirePaused(&req.Request, 1004) {
		return
	}
	frames, err := s.rt.Stack(req.Arguments.ThreadId)
	if err != nil {
		s.sendError(&req.Request, 1004, err.Error())
		return
	}
	out := make([]dap.StackFrame, 0, len(frames))
	for _, f := range frames {
		out = append(out, dap.StackFrame{
			Id:     f.ID,
			Name:   f.Name,
			Source: &dap.Source{Name: filepath.Base(f.File), Path: f.File},
			Line:   f.Line,
			Column: 1,
		})
	}
	s.send(&dap.StackTraceResponse{
		Response: s.newResponse(req.Request),
		Body:     dap.StackTraceResponseBody{StackFrames: out, TotalFrames: len(out)},
	})
}

func (s *Server) onScopes(req *dap.ScopesRequest) {
	if !s.requirePaused(&req.Request, 1005) {
		return
	}
	scopes, err := s.rt.Scopes(req.Arguments.FrameId)
	if err != nil {
		s.sendError(&req.Request, 1005, err.Error())
		return
	}
	out := make([]dap.Scope, 0, len(scopes))
	for _, sc := range scopes {
		out = append(out, dap.Scope{Name: sc.Name, VariablesReference: sc.VariablesReference})
	}
	s.send(&dap.ScopesResponse{Response: s.newResponse(req.Request), Body: dap.ScopesResponseBody{Scopes: out}})
}

func (s *Server) onVariables(req *dap.VariablesRequest) {
	if !s.requirePaused(&req.Request, 1006) {
		return
	}
	vars, err := s.rt.Variables(req.Arguments.VariablesReference)
	if err != nil {
		s.sendError(&req.Request, 1006, err.Error())
		return
	}
	out := make([]dap.Variable, 0, len(vars))
	for _, v := range vars {
		out = append(out, dap.Variable{Name: v.Name, Value: v.Value, Type: v.Type, VariablesReference: v.VariablesReference})
	}
	s.send(&dap.VariablesResponse{Response: s.newResponse(req.Request), Body: dap.VariablesResponseBody{Variables: out}})
}

func (s *Server) onEvaluate(req *dap.EvaluateRequest) {
	if !s.requirePaused(&req.Request, 1007) {
		return
	}
	result, err := s.rt.Evaluate(req.Arguments.Expression, req.Arguments.FrameId)
	if err != nil {
		s.sendError(&req.Request, 1007, err.Error())
		return
	}
	s.send(&dap.EvaluateResponse{
		Response: s.newResponse(req.Request),
		Body:     dap.EvaluateResponseBody{Result: result.Result, Type: result.Type, VariablesReference: result.VariablesReference},
	})
}

func (s *Server) onResume(req dap.Request, thread int, cmd string, response func() dap.Message) {
	if !s.requirePaused(&req, 1008) {
		return
	}
	if err := s.rt.Resume(cmd, map[string]any{"threadId": thread}); err != nil {
		s.sendError(&req, 1008, err.Error())
		return
	}
	s.send(response())
}

func (s *Server) toDapBreakpoints(file string, verified []VerifiedBreakpoint) []dap.Breakpoint {
	s.bpMu.Lock()
	defer s.bpMu.Unlock()
	ids := s.bpIDs[file]
	if ids == nil {
		ids = map[int]int{}
		s.bpIDs[file] = ids
	}
	out := make([]dap.Breakpoint, 0, len(verified))
	for _, bp := range verified {
		id, ok := ids[bp.Line]
		if !ok {
			id = s.nextBpID
			s.nextBpID++
			ids[bp.Line] = id
		}
		out = append(out, dap.Breakpoint{Id: id, Verified: bp.Verified, Message: bp.Message, Line: bp.Line})
	}
	return out
}

func (s *Server) t(key string, args ...any) string {
	return i18n.T(s.locale, key, args...)
}

// --- message plumbing ---------------------------------------------------------

func (s *Server) newResponse(req dap.Request) dap.Response {
	return dap.Response{
		ProtocolMessage: dap.ProtocolMessage{Type: "response"},
		RequestSeq:      req.Seq,
		Success:         true,
		Command:         req.Command,
	}
}

func (s *Server) newEvent(name string) dap.Event {
	return dap.Event{ProtocolMessage: dap.ProtocolMessage{Type: "event"}, Event: name}
}

// sendError answers a request with an error response; VS Code shows `format` to the user.
func (s *Server) sendError(req *dap.Request, id int, text string) {
	resp := s.newResponse(*req)
	resp.Success = false
	resp.Message = text
	s.send(&dap.ErrorResponse{
		Response: resp,
		Body:     dap.ErrorResponseBody{Error: &dap.ErrorMessage{Id: id, Format: text, ShowUser: true}},
	})
}

// send assigns the outgoing seq and writes one framed message.
func (s *Server) send(msg dap.Message) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.seq++
	switch m := msg.(type) {
	case dap.ResponseMessage:
		m.GetResponse().Seq = s.seq
	case dap.EventMessage:
		m.GetEvent().Seq = s.seq
	}
	if err := dap.WriteProtocolMessage(s.writer, msg); err != nil {
		log.Printf("write failed: %v", err)
	}
}
