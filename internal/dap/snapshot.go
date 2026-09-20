package dap

import (
	"bytes"
	"encoding/json"

	"github.com/google/go-dap"
)

type snapshotRequest struct {
	dap.Request
	Arguments struct {
		VariablesReference int `json:"variablesReference"`
	} `json:"arguments"`
}

type snapshotResponse struct {
	dap.Response
	Body snapshotInfo `json:"body"`
}

type snapshotInfo struct {
	JSON string `json:"json"`
}

func (r *Runtime) snapshot(ref int) (snapshotInfo, error) {
	var result snapshotInfo
	err := r.call("snapshot", map[string]any{"ref": ref}, &result)
	return result, err
}

func (s *Server) onSnapshot(req *snapshotRequest) {
	if !s.requirePaused(&req.Request, 1009) {
		return
	}
	result, err := s.rt.snapshot(req.Arguments.VariablesReference)
	if err == nil {
		var pretty bytes.Buffer
		err = json.Indent(&pretty, []byte(result.JSON), "", "  ")
		result.JSON = pretty.String() + "\n"
	}
	if err != nil {
		s.sendError(&req.Request, 1009, err.Error())
		return
	}
	s.send(&snapshotResponse{Response: s.newResponse(req.Request), Body: result})
}
