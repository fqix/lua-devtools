package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fqix/lua-devtools/internal/analysis"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestRefactoringProtocol(t *testing.T) {
	root := t.TempDir()
	uri := pathFileURI(filepath.Join(root, "main.lua"))
	text := []byte("local value=1\ndo local value=2; print(value) end\nprint(value)")
	file := analysis.Parse(text)
	defer file.Close()
	server := &Server{docs: map[string]*document{uri: {uri: uri, text: text, file: file, version: 7}}}
	position := protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: protocol.Position{Character: 7}}
	for _, include := range []bool{false, true} {
		locations, err := server.references(nil, &protocol.ReferenceParams{TextDocumentPositionParams: position, Context: protocol.ReferenceContext{IncludeDeclaration: include}})
		want := 1
		if include {
			want = 2
		}
		if err != nil || len(locations) != want {
			t.Fatalf("references %+v %v", locations, err)
		}
	}
	prepared, err := server.prepareRename(nil, &protocol.PrepareRenameParams{TextDocumentPositionParams: position})
	if err != nil || prepared.(protocol.RangeWithPlaceholder).Placeholder != "value" {
		t.Fatalf("prepare %+v %v", prepared, err)
	}
	edit, err := server.rename(nil, &protocol.RenameParams{TextDocumentPositionParams: position, NewName: "total"})
	if err != nil {
		t.Fatal(err)
	}
	change := edit.DocumentChanges[0].(protocol.TextDocumentEdit)
	if *change.TextDocument.Version != 7 || len(change.Edits) != 2 {
		t.Fatalf("rename %+v", change)
	}
	if _, err := server.rename(nil, &protocol.RenameParams{TextDocumentPositionParams: position, NewName: "print"}); err == nil {
		t.Fatal("captured global function")
	}
}

func TestModuleSignatureHelp(t *testing.T) {
	root, library := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(library, "mod.lua"), []byte("local M={}\nfunction M.run(first,second,...) end\nreturn M"), 0600); err != nil {
		t.Fatal(err)
	}
	uri := pathFileURI(filepath.Join(root, "main.lua"))
	text := []byte(`local m=require("mod"); m.run(1, 2)`)
	file := analysis.Parse(text)
	defer file.Close()
	server := &Server{workspaceRoots: []string{root}, modulePaths: []moduleSearchPath{{Root: root, Templates: []string{filepath.Join(library, "?.lua")}}}, docs: map[string]*document{uri: {uri: uri, text: text, file: file}}}
	help, err := server.signatureHelp(nil, &protocol.SignatureHelpParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: toPosition(file.PositionOf(strings.Index(string(text), "2)")))}})
	if err != nil || help == nil || help.Signatures[0].Label != "m.run(first, second, ...)" || *help.ActiveParameter != 1 {
		t.Fatalf("signature %+v %v", help, err)
	}
}
