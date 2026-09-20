package lsp

import (
	"reflect"
	"testing"

	"github.com/fqix/lua-devtools/internal/analysis"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestLuaUnitCodeLens(t *testing.T) {
	t.Parallel()
	const uri = "file:///tests/unit.lua"
	f := analysis.Parse([]byte("local lu = require('luaunit')\nTestFoo = {}\nfunction TestFoo:testBar() end\n"))
	defer f.Close()
	server := &Server{locale: "zh-cn", docs: map[string]*document{uri: {uri: uri, file: f}}}
	lenses, err := server.codeLens(nil, &protocol.CodeLensParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
	if err != nil {
		t.Fatal(err)
	}
	if len(lenses) != 4 {
		t.Fatalf("lenses: %+v", lenses)
	}
	if lenses[0].Command.Command != CommandRun || lenses[1].Command.Command != CommandDebug {
		t.Fatal("file actions missing")
	}
	for i, expected := range []struct{ command, title string }{{CommandRunTest, "$(beaker) 运行测试"}, {CommandDebugTest, "$(debug-alt) 调试测试"}} {
		lens := lenses[i+2]
		if lens.Range.Start.Line != 2 || lens.Command.Command != expected.command || lens.Command.Title != expected.title || !reflect.DeepEqual(lens.Command.Arguments, []any{uri, "TestFoo.testBar", "luaunit"}) {
			t.Errorf("incorrect test lens: %+v, command %+v", lens, lens.Command)
		}
	}
}
