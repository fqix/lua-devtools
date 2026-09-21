package lsp

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/fqix/lua-devtools/internal/analysis"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

// referenceWorkspace builds a project with a module on disk, an open consumer,
// a closed consumer and files that a scan must skip.
func referenceWorkspace(t *testing.T) (*Server, string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"lib/mod.lua":        "local M = {}\nM.__index = M\nfunction M.run() end\nM.count = 0\nfunction M:each() end\nreturn M\n",
		"other.lua":          "local m = require('lib.mod')\nm.run()\nlocal obj = setmetatable({}, m)\nobj:each()\ngreet()\nlocal unrelated = { run = 1 }\nunrelated.run()\n",
		"node_modules/x.lua": "local m = require('lib.mod'); m.run()",
		".lua-devtools/rocks/share/lua/5.4/dep.lua": "local m = require('lib.mod'); m.run()",
	}
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	main := "local m = require('lib.mod')\nfunction greet() end\nm.run()\nprint(m.count)\ngreet()\n"
	uri := pathFileURI(filepath.Join(root, "main.lua"))
	file := analysis.Parse([]byte(main))
	t.Cleanup(file.Close)
	server := &Server{workspaceRoots: []string{root}, docs: map[string]*document{uri: {uri: uri, text: []byte(main), file: file, version: 3}}}
	files["main.lua"] = main
	return server, root, files
}

func locate(t *testing.T, server *Server, uri, text, needle string, skip int) protocol.TextDocumentPositionParams {
	t.Helper()
	index := strings.Index(text, needle)
	f := analysis.Parse([]byte(text))
	defer f.Close()
	return protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: toPosition(f.PositionOf(index + skip))}
}

func summarize(root string, locations []protocol.Location) []string {
	out := []string{}
	for _, location := range locations {
		rel, _ := filepath.Rel(root, fileURIPath(location.URI))
		out = append(out, filepath.ToSlash(rel)+":"+itoa(int(location.Range.Start.Line)+1))
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func TestWorkspaceMemberReferences(t *testing.T) {
	server, root, files := referenceWorkspace(t)
	uri := pathFileURI(filepath.Join(root, "main.lua"))
	position := locate(t, server, uri, files["main.lua"], "m.run", 2)
	for _, include := range []bool{true, false} {
		locations, err := server.references(nil, &protocol.ReferenceParams{TextDocumentPositionParams: position, Context: protocol.ReferenceContext{IncludeDeclaration: include}})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"main.lua:3", "other.lua:2"}
		if include {
			want = append([]string{"lib/mod.lua:3"}, want...)
		}
		got := summarize(root, locations)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("include=%v references %v, want %v", include, got, want)
		}
	}
	// A method reached through the module's __index from an instance.
	moduleURI := pathFileURI(filepath.Join(root, "lib", "mod.lua"))
	moduleFile := analysis.Parse([]byte(files["lib/mod.lua"]))
	defer moduleFile.Close()
	server.docs[moduleURI] = &document{uri: moduleURI, text: []byte(files["lib/mod.lua"]), file: moduleFile}
	locations, err := server.references(nil, &protocol.ReferenceParams{TextDocumentPositionParams: locate(t, server, moduleURI, files["lib/mod.lua"], "M:each", 2), Context: protocol.ReferenceContext{IncludeDeclaration: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := summarize(root, locations); strings.Join(got, " ") != "lib/mod.lua:5 other.lua:4" {
		t.Fatalf("method references %v", got)
	}
	// Data fields navigate to their declaration.
	definition, err := server.definition(nil, &protocol.DefinitionParams{TextDocumentPositionParams: locate(t, server, uri, files["main.lua"], "m.count", 2)})
	if err != nil {
		t.Fatal(err)
	}
	if location, ok := definition.(protocol.Location); !ok || location.URI != moduleURI || location.Range.Start.Line != 3 {
		t.Fatalf("count definition %+v", definition)
	}
}

func TestWorkspaceMemberRename(t *testing.T) {
	server, root, files := referenceWorkspace(t)
	uri := pathFileURI(filepath.Join(root, "main.lua"))
	position := locate(t, server, uri, files["main.lua"], "m.run", 2)
	prepared, err := server.prepareRename(nil, &protocol.PrepareRenameParams{TextDocumentPositionParams: position})
	if err != nil || prepared.(protocol.RangeWithPlaceholder).Placeholder != "run" {
		t.Fatalf("prepare %+v %v", prepared, err)
	}
	edit, err := server.rename(nil, &protocol.RenameParams{TextDocumentPositionParams: position, NewName: "start"})
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]protocol.TextDocumentEdit{}
	for _, change := range edit.DocumentChanges {
		documentEdit := change.(protocol.TextDocumentEdit)
		rel, _ := filepath.Rel(root, fileURIPath(documentEdit.TextDocument.URI))
		changes[filepath.ToSlash(rel)] = documentEdit
	}
	if len(changes) != 3 || len(changes["lib/mod.lua"].Edits) != 1 || len(changes["main.lua"].Edits) != 1 || len(changes["other.lua"].Edits) != 1 {
		t.Fatalf("rename edits %+v", changes)
	}
	if changes["lib/mod.lua"].TextDocument.Version != nil || *changes["main.lua"].TextDocument.Version != 3 {
		t.Fatalf("document versions %+v", changes)
	}
	for _, newName := range []string{"count", "each", "__index"} {
		if _, err := server.rename(nil, &protocol.RenameParams{TextDocumentPositionParams: position, NewName: newName}); err == nil {
			t.Fatalf("rename to declared member %q must fail", newName)
		}
	}
	if _, err := server.rename(nil, &protocol.RenameParams{TextDocumentPositionParams: position, NewName: "end"}); err == nil {
		t.Fatal("keyword must fail")
	}
}

func TestWorkspaceGlobalReferencesAndRename(t *testing.T) {
	server, root, files := referenceWorkspace(t)
	uri := pathFileURI(filepath.Join(root, "main.lua"))
	position := locate(t, server, uri, files["main.lua"], "greet()", 0)
	locations, err := server.references(nil, &protocol.ReferenceParams{TextDocumentPositionParams: position, Context: protocol.ReferenceContext{IncludeDeclaration: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := summarize(root, locations); strings.Join(got, " ") != "main.lua:2 main.lua:5 other.lua:5" {
		t.Fatalf("global references %v", got)
	}
	locations, err = server.references(nil, &protocol.ReferenceParams{TextDocumentPositionParams: position, Context: protocol.ReferenceContext{IncludeDeclaration: false}})
	if err != nil || len(locations) != 2 {
		t.Fatalf("global uses %+v %v", locations, err)
	}
	edit, err := server.rename(nil, &protocol.RenameParams{TextDocumentPositionParams: position, NewName: "hello"})
	if err != nil || len(edit.DocumentChanges) != 2 {
		t.Fatalf("global rename %+v %v", edit, err)
	}
	// `obj` is a local in other.lua that would capture the renamed global there.
	if _, err := server.rename(nil, &protocol.RenameParams{TextDocumentPositionParams: position, NewName: "obj"}); err == nil {
		t.Fatal("captured by a local in another file")
	}
	if _, err := server.rename(nil, &protocol.RenameParams{TextDocumentPositionParams: position, NewName: "print"}); err == nil {
		t.Fatal("collides with a global used elsewhere")
	}
}

func TestWorkspaceRenameRefusesDependencyMembers(t *testing.T) {
	server, root, files := referenceWorkspace(t)
	// A module reachable only through the environment's package tree.
	library := filepath.Join(root, ".lua-devtools", "rocks", "share", "lua", "5.4")
	if err := os.MkdirAll(library, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(library, "dep.lua"), []byte("local D = {}\nfunction D.run() end\nreturn D\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.modulePaths = []moduleSearchPath{{Root: root, Templates: []string{filepath.Join(library, "?.lua")}}}
	uri := pathFileURI(filepath.Join(root, "main.lua"))
	main := "local d = require('dep')\nd.run()\n"
	file := analysis.Parse([]byte(main))
	defer file.Close()
	server.docs[uri] = &document{uri: uri, text: []byte(main), file: file, version: 1}
	files["main.lua"] = main
	position := locate(t, server, uri, main, "d.run", 2)
	if _, err := server.rename(nil, &protocol.RenameParams{TextDocumentPositionParams: position, NewName: "start"}); err == nil {
		t.Fatal("renaming a member declared in an installed package must fail")
	}
	locations, err := server.references(nil, &protocol.ReferenceParams{TextDocumentPositionParams: position, Context: protocol.ReferenceContext{IncludeDeclaration: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := summarize(root, locations); strings.Join(got, " ") != ".lua-devtools/rocks/share/lua/5.4/dep.lua:2 main.lua:2" {
		t.Fatalf("dependency references %v", got)
	}
}

func TestWorkspaceRenameRefusesRebinding(t *testing.T) {
	server, root, _ := referenceWorkspace(t)
	uri := pathFileURI(filepath.Join(root, "rebind.lua"))
	text := "local t = {foo = 1}\nif os.getenv('X') then t = {foo = 2} end\nprint(t.foo)\n"
	file := analysis.Parse([]byte(text))
	defer file.Close()
	server.docs[uri] = &document{uri: uri, text: []byte(text), file: file, version: 1}
	position := locate(t, server, uri, text, "{foo = 1}", 1)
	if _, err := server.rename(nil, &protocol.RenameParams{TextDocumentPositionParams: position, NewName: "bar"}); err == nil {
		t.Fatal("undecidable binding must refuse rename")
	}
	locations, err := server.references(nil, &protocol.ReferenceParams{TextDocumentPositionParams: position, Context: protocol.ReferenceContext{IncludeDeclaration: true}})
	if err != nil || len(locations) != 1 {
		t.Fatalf("references %+v %v", locations, err)
	}
}

func TestWorkspaceRenameRefusesUncertainTableAliases(t *testing.T) {
	for name, source := range map[string]string{
		"nested replacement":        "local t={child={foo=1}}\nprint(t.child.foo)\nt.child={foo=2}\nprint(t.child.foo)",
		"replacement through alias": "local t={child={foo=1}}\nlocal alias=t\nprint(t.child.foo)\nalias.child={foo=2}\nprint(t.child.foo)",
		"branch alias":              "local t={foo=1}\nif flag then t={foo=2} end\nlocal alias=t\nprint(alias.foo)",
		"alias chain":               "local t={foo=1}\nif flag then t={foo=2} end\nlocal a=t\nlocal b=a\nprint(b.foo)",
		"nested alias":              "local t={child={foo=1}}\nif flag then t={child={foo=2}} end\nlocal alias=t.child\nprint(alias.foo)",
		"function alias":            "local t={foo=1}\nlocal function reset() t={foo=2} end\nlocal alias=t\nprint(alias.foo)",
		"loop alias":                "local t={foo=1}\nwhile flag do local alias=t; print(alias.foo); t={foo=2} end",
	} {
		t.Run(name, func(t *testing.T) {
			server, root, _ := referenceWorkspace(t)
			uri := pathFileURI(filepath.Join(root, "alias.lua"))
			file := analysis.Parse([]byte(source))
			defer file.Close()
			server.docs[uri] = &document{uri: uri, text: []byte(source), file: file}
			// Test both the declaration and a use, so unsafe renames cannot
			// slip through by starting from another view of the same member.
			for _, needle := range []string{"foo=1", "foo)"} {
				position := locate(t, server, uri, source, needle, 0)
				_, err := server.rename(nil, &protocol.RenameParams{TextDocumentPositionParams: position, NewName: "bar"})
				if err == nil || !strings.Contains(err.Error(), "rename is unsafe") {
					t.Fatalf("rename at %q must reject an uncertain table binding, got %v", needle, err)
				}
			}
		})
	}
}

func TestWorkspaceGlobalRenameChecksDeclarationScope(t *testing.T) {
	for _, test := range []struct {
		name, source string
		conflict     bool
	}{
		{"local capture", "local hello=1\nfunction greet() end", true},
		{"block capture", "do local hello=1; function greet() end end", true},
		{"parameter capture", "local function outer(hello) function greet() end end", true},
		{"assignment capture", "local hello=1\ngreet=2", true},
		{"anonymous function use", "local f=function(hello) greet() end", true},
		{"own parameter", "function greet(hello) end", false},
		{"later local", "function greet() end\nlocal hello=1", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			uri := pathFileURI(filepath.Join(root, "main.lua"))
			file := analysis.Parse([]byte(test.source))
			defer file.Close()
			server := &Server{workspaceRoots: []string{root}, docs: map[string]*document{uri: {uri: uri, text: []byte(test.source), file: file}}}
			position := locate(t, server, uri, test.source, "greet", 0)
			edit, err := server.rename(nil, &protocol.RenameParams{TextDocumentPositionParams: position, NewName: "hello"})
			if test.conflict {
				if err == nil {
					t.Fatalf("rename must reject capture, got %+v", edit)
				}
			} else if err != nil || len(edit.DocumentChanges) != 1 {
				t.Fatalf("safe rename: %+v, %v", edit, err)
			}
		})
	}
}
