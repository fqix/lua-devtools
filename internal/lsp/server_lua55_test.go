package lsp

import (
	"strings"
	"testing"

	"github.com/fqix/lua-devtools/internal/analysis"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestLua55DefinitionAndDocumentSymbols(t *testing.T) {
	t.Parallel()
	source := "local x=0\ndo\n global <const> x=1\n global function f(...args)\n  local value=args.n+x\n  return value\n end\nend\nlocal result=x\n"
	f := analysis.Parse([]byte(source))
	defer f.Close()
	server := &Server{docs: map[string]*document{"test": {uri: "test", file: f}}}
	for _, tt := range []struct{ name, use, declaration string }{
		{"explicit global", "x\n  return", "x=1"},
		{"named vararg", "args.n", "args)"},
		{"outer local restored", "x\n", "x=0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			at := strings.LastIndex(source, tt.use)
			result, err := server.definition(nil, &protocol.DefinitionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{
				TextDocument: protocol.TextDocumentIdentifier{URI: "test"}, Position: toPosition(f.PositionOf(at)),
			}})
			location, ok := result.(protocol.Location)
			want := toPosition(f.PositionOf(strings.Index(source, tt.declaration)))
			if err != nil || !ok || location.URI != "test" || location.Range.Start != want {
				t.Fatalf("definition=%+v err=%v, want %+v", result, err, want)
			}
		})
	}
	result, err := server.documentSymbol(nil, &protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	symbols := result.([]protocol.DocumentSymbol)
	if len(symbols) != 4 {
		t.Fatalf("outline=%+v", symbols)
	}
	fn := symbols[2]
	if fn.Name != "f" || fn.Kind != protocol.SymbolKindFunction || fn.Detail == nil || *fn.Detail != "global function f(...args)" {
		t.Fatalf("function=%+v", fn)
	}
	if fn.Range.Start != (protocol.Position{Line: 3, Character: 1}) || fn.Range.End != (protocol.Position{Line: 6, Character: 4}) || fn.SelectionRange.Start != (protocol.Position{Line: 3, Character: 17}) {
		t.Fatalf("ranges=%+v", fn)
	}
	if len(fn.Children) != 1 || fn.Children[0].Name != "value" {
		t.Fatalf("children=%+v", fn.Children)
	}
}
