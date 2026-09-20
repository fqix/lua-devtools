package dap

import (
	"io"
	"os"
	"time"
	"unicode/utf8"
)

// protocolReader tails a regular file until the child exits. Unlike inherited
// extra file descriptors, this also works with external Lua on Windows.
type protocolReader struct {
	file *os.File
	done <-chan struct{}
}

func (r *protocolReader) Read(p []byte) (int, error) {
	n, err := r.file.Read(p)
	if n > 0 || err != io.EOF {
		return n, err
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			// A final write may have arrived between Read and process exit.
			return r.file.Read(p)
		case <-ticker.C:
			n, err := r.file.Read(p)
			if n > 0 || err != io.EOF {
				return n, err
			}
		}
	}
}

// os/exec serializes writes to each stream and Wait joins the copy goroutines.
// Keep an incomplete UTF-8 suffix for the next write so Unicode split across
// pipe reads is not replaced by JSON encoding.
type outputStream struct {
	runtime  *Runtime
	category string
	pending  []byte
}

func (s *outputStream) Write(p []byte) (int, error) {
	s.pending = append(s.pending, p...)
	end := 0
	for end < len(s.pending) && utf8.FullRune(s.pending[end:]) {
		_, size := utf8.DecodeRune(s.pending[end:])
		end += size
	}
	if end > 0 {
		s.runtime.Events <- Event{Kind: "output", Category: s.category, Text: string(s.pending[:end])}
		s.pending = append(s.pending[:0], s.pending[end:]...)
	}
	return len(p), nil
}

func (s *outputStream) flush() {
	if len(s.pending) > 0 {
		s.runtime.Events <- Event{Kind: "output", Category: s.category, Text: string(s.pending)}
		s.pending = s.pending[:0]
	}
}
