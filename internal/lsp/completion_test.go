package lsp

import (
	"slices"
	"strings"
	"testing"

	"github.com/fqix/lua-devtools/internal/analysis"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestMemberCompletion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, source string
		want, absent []string
	}{
		{name: "standard library", source: "math.", want: []string{"abs", "pi"}, absent: []string{"print"}},
		{name: "typed prefix", source: "string.su", want: []string{"sub"}, absent: []string{"print"}},
		{name: "parameter shadows library", source: "local function f(math)\nmath.|\nend", absent: []string{"abs", "print"}},
		{name: "shadow library", source: "local math = {mine = 1}\nmath.", want: []string{"mine"}, absent: []string{"abs"}},
		{name: "colon functions only", source: "local m = {x = 1, run = function() end}\nm:", want: []string{"run"}, absent: []string{"x", "print"}},
		{name: "spaces", source: "local m = {x = 1}\nm . ", want: []string{"x"}},
		{name: "nested", source: "local m = {child = {x = 1}}\nm.child.", want: []string{"x"}, absent: []string{"child"}},
		{name: "unknown receiver", source: "unknown.", absent: []string{"print", "abs"}},
		{name: "comment", source: "-- math.", absent: []string{"abs", "print"}},
		{name: "string", source: `local s = "math.|"`, absent: []string{"abs", "print"}},
		{name: "concat", source: `return "" ..`, absent: []string{"abs", "print"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			offset := strings.IndexByte(tt.source, '|')
			source := strings.Replace(tt.source, "|", "", 1)
			if offset < 0 {
				offset = len(source)
			}
			f := analysis.Parse([]byte(source))
			defer f.Close()
			s := &Server{docs: map[string]*document{"test": {file: f}}}
			result, err := s.completion(nil, &protocol.CompletionParams{
				TextDocumentPositionParams: protocol.TextDocumentPositionParams{
					TextDocument: protocol.TextDocumentIdentifier{URI: "test"},
					Position:     toPosition(f.PositionOf(offset)),
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			labels := []string{}
			for _, item := range result.(protocol.CompletionList).Items {
				labels = append(labels, item.Label)
			}
			for _, label := range tt.want {
				if !slices.Contains(labels, label) {
					t.Errorf("missing %q in %v", label, labels)
				}
			}
			for _, label := range tt.absent {
				if slices.Contains(labels, label) {
					t.Errorf("unexpected %q in %v", label, labels)
				}
			}
		})
	}
}
