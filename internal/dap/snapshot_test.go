package dap

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/go-dap"
)

func TestSnapshotRequestRequiresPausedSession(t *testing.T) {
	request := `{"seq":1,"type":"request","command":"lua/snapshot","arguments":{"variablesReference":1}}`
	var input, output bytes.Buffer
	fmt.Fprintf(&input, "Content-Length: %d\r\n\r\n%s", len(request), request)
	server := NewServer(&input, &output, Options{})
	if err := server.Run(); err != nil {
		t.Fatal(err)
	}
	body, err := dap.ReadBaseMessage(bufio.NewReader(&output))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Command    string
		Success    bool
		RequestSeq int `json:"request_seq"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if response.Command != "lua/snapshot" || response.Success || response.RequestSeq != 1 {
		t.Fatalf("response=%s", body)
	}
}
