package dap

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/google/go-dap"
)

type AttachArguments struct {
	Host                   string `json:"host"`
	Port                   int    `json:"port"`
	Token                  string `json:"token"`
	Cwd                    string `json:"cwd"`
	StopOnEntry            bool   `json:"stopOnEntry"`
	BreakOnCoroutineErrors bool   `json:"breakOnCoroutineErrors"`
}

// Attach connects to a cooperatively instrumented Lua host. Closing the transport
// detaches hooks; it never kills the embedding process.
func (r *Runtime) Attach(options AttachArguments) error {
	if options.Host == "" {
		options.Host = "127.0.0.1"
	}
	if options.Port < 1 || options.Port > 65535 {
		return errors.New("attach requires a TCP port between 1 and 65535")
	}
	connection, err := net.DialTimeout("tcp", net.JoinHostPort(options.Host, fmt.Sprint(options.Port)), 5*time.Second)
	if err != nil {
		return err
	}
	_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(connection).Encode(map[string]string{"token": options.Token}); err != nil {
		connection.Close()
		return err
	}
	_ = connection.SetWriteDeadline(time.Time{})
	r.connection, r.stdin = connection, connection
	r.mu.Lock()
	r.paused = true
	r.mu.Unlock()
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case line := <-r.commands:
				if _, err := connection.Write(line); err != nil {
					connection.Close()
					return
				}
			}
		}
	}()
	go func() {
		r.readProtocol(connection)
		connection.Close()
		close(done)
		r.mu.Lock()
		r.closed = true
		for id, ch := range r.pending {
			ch <- reply{err: errors.New("Lua debugger detached")}
			delete(r.pending, id)
		}
		r.mu.Unlock()
		r.Events <- Event{Kind: "exit"}
	}()
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
		return errors.New("Lua attach handshake timed out")
	}
	r.mu.Lock()
	r.liveControl = capabilities.LiveControl
	r.mu.Unlock()
	go r.flushPendingBreakpoints()
	return nil
}

func (s *Server) onAttach(req *dap.AttachRequest) {
	var options AttachArguments
	if err := json.Unmarshal(req.Arguments, &options); err != nil {
		s.sendError(&req.Request, 1001, err.Error())
		return
	}
	if s.rt == nil {
		s.rt = NewRuntime()
	}
	if err := s.rt.Attach(options); err != nil {
		s.sendError(&req.Request, 1001, err.Error())
		return
	}
	s.stopOnEntry, s.cwd = options.StopOnEntry, options.Cwd
	s.breakOnCoroutineErrors = options.BreakOnCoroutineErrors
	go s.pumpEvents(s.rt)
	s.send(&dap.AttachResponse{Response: s.newResponse(req.Request)})
}

type inputRequest struct {
	dap.Request
	Arguments struct {
		Text string `json:"text"`
		EOF  bool   `json:"eof"`
	} `json:"arguments"`
}

type inputChunk struct {
	text string
	eof  bool
}

type resumeCoroutineRequest struct {
	dap.Request
	Arguments struct {
		ThreadID int `json:"threadId"`
	} `json:"arguments"`
}

func (s *Server) onResumeCoroutine(req *resumeCoroutineRequest) {
	if !s.requirePaused(&req.Request, 1008) {
		return
	}
	if err := s.rt.Resume("resumeThread", map[string]any{"threadId": req.Arguments.ThreadID}); err != nil {
		s.sendError(&req.Request, 1008, err.Error())
		return
	}
	response := s.newResponse(req.Request)
	s.send(&response)
}

func (r *Runtime) Input(text string, eof bool) error {
	r.inputMu.Lock()
	defer r.inputMu.Unlock()
	if r.input == nil {
		return errors.New("program input requires a launch session with interactive: true")
	}
	if len(text) > 65536 {
		return errors.New("input exceeds 64 KiB")
	}
	select {
	case r.inputQueue <- inputChunk{text: text, eof: eof}:
		if eof {
			r.input = nil
		}
	default:
		return errors.New("program input queue is full")
	}
	return nil
}

func (s *Server) onInput(req *inputRequest) {
	if s.rt == nil {
		s.sendError(&req.Request, 1002, "Lua process is not running")
		return
	}
	if err := s.rt.Input(req.Arguments.Text, req.Arguments.EOF); err != nil {
		s.sendError(&req.Request, 1002, err.Error())
		return
	}
	response := s.newResponse(req.Request)
	s.send(&response)
}
