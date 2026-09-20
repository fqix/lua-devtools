package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fqix/lua-devtools/internal/analysis"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestRequiredFunctionDefinitions(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, source, module, modulePath, target string
		unsaved                                  bool
	}{
		{"function", `local m=require("lib.mod"); m.r|un()`, "local M={}\nfunction M.run() end\nreturn M", "lib/mod.lua", "run", false},
		{"inside function", `local m=require("lib.mod"); local function checkout() return m.r|un() end`, "local M={}\nfunction M.run() end\nreturn M", "lib/mod.lua", "run", false},
		{"method", `local m=require("lib.mod"); m:r|un()`, "local M={}\nfunction M:run() end\nreturn M", "lib/mod.lua", "run", false},
		{"table alias", `local m=require("lib.mod"); local a=m; a.r|un()`, "local M={}\nfunction M.run() end\nreturn M", "lib/mod.lua", "run", false},
		{"function alias", `local m=require("lib.mod"); local f=m.run; f|()`, "local M={}\nfunction M.run() end\nreturn M", "lib/mod.lua", "run", false},
		{"directory module", `local m=require("lib.mod"); m.r|un()`, "return {run=function() end}", "lib/mod/init.lua", "function", false},
		{"exported local", `local m=require("lib.mod"); m.r|un()`, "local function impl() end\nreturn {run=impl}", "lib/mod.lua", "impl", false},
		{"unsaved module", `local m=require("lib.mod"); m.r|un()`, "\n\nlocal M={}\nfunction M.run() end\nreturn M", "lib/mod.lua", "run", true},
		{"shadowed receiver", `local m=require("lib.mod"); do local m={}; m.r|un() end`, "return {run=function() end}", "lib/mod.lua", "", false},
		{"reassigned receiver", `local m=require("lib.mod"); m={}; m.r|un()`, "return {run=function() end}", "lib/mod.lua", "", false},
		{"shadowed require", `local require=function() return {} end; local m=require("lib.mod"); m.r|un()`, "return {run=function() end}", "lib/mod.lua", "", false},
		{"cycle", `local m=require("lib.mod"); m.r|un()`, `return require("lib.mod")`, "lib/mod.lua", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "examples", tt.modulePath)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.module), 0600); err != nil {
				t.Fatal(err)
			}
			source := strings.Replace(tt.source, "|", "", 1)
			f := analysis.Parse([]byte(source))
			defer f.Close()
			uri := pathFileURI(filepath.Join(root, "examples", "main.lua"))
			s := &Server{workspaceRoots: []string{root}, docs: map[string]*document{
				uri: {uri: uri, text: []byte(source), file: f},
			}}
			if tt.unsaved {
				if err := os.WriteFile(path, []byte("return {}"), 0600); err != nil {
					t.Fatal(err)
				}
				s.docs[pathFileURI(path)] = &document{text: []byte(tt.module)}
			}
			got, err := s.definition(nil, &protocol.DefinitionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{
				TextDocument: protocol.TextDocumentIdentifier{URI: uri},
				Position:     toPosition(f.PositionOf(strings.Index(tt.source, "|"))),
			}})
			if err != nil {
				t.Fatal(err)
			}
			if tt.target == "" {
				if got != nil {
					t.Fatalf("unexpected definition: %+v", got)
				}
				return
			}
			location, ok := got.(protocol.Location)
			if !ok {
				t.Fatalf("missing definition: %+v", got)
			}
			module := analysis.Parse([]byte(tt.module))
			defer module.Close()
			want := toPosition(module.PositionOf(strings.Index(tt.module, tt.target)))
			if location.URI != pathFileURI(path) || location.Range.Start != want {
				t.Fatalf("got %+v, want %s at %+v", location, path, want)
			}
		})
	}
}

func TestForwardedDefinitionKeepsOriginalLocation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for name, source := range map[string]string{
		"original.lua": "local M={}\nfunction M.run() end\nreturn M",
		"forward.lua":  `return require("original")`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{docs: map[string]*document{}, workspaceRoots: []string{root}}
	members := s.moduleResolver(pathFileURI(filepath.Join(root, "main.lua")), true)("forward")
	if len(members) != 1 || members[0].Definition == nil || members[0].Definition.URI != pathFileURI(filepath.Join(root, "original.lua")) {
		t.Fatalf("forwarded definition: %+v", members)
	}
}
