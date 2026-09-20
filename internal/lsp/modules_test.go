package lsp

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fqix/lua-devtools/internal/analysis"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestModuleExports(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("mod.lua", `local M={value=1}; function M.run() end; return M`)
	write("pkg/init.lua", `return {nested=1}`)
	write("forward.lua", `return require("mod")`)
	write("cycleA.lua", `local x=require("cycleB"); return x`)
	write("cycleB.lua", `return require("cycleA")`)
	write("shadow.lua", `local require=function() end; return require("mod")`)
	uri := func(path string) string {
		path = filepath.ToSlash(path)
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		return (&url.URL{Scheme: "file", Path: path}).String()
	}
	server := &Server{docs: map[string]*document{}, workspaceRoots: []string{root}}
	resolver := server.moduleResolver(uri(filepath.Join(root, "main.lua")))
	for _, tt := range []struct {
		name string
		want []analysis.Member
	}{
		{"mod", []analysis.Member{{Name: "run", Function: true}, {Name: "value"}}},
		{"pkg", []analysis.Member{{Name: "nested"}}},
		{"forward", []analysis.Member{{Name: "run", Function: true}, {Name: "value"}}},
		{"cycleA", []analysis.Member{}},
		{"shadow", []analysis.Member{}},
		{"../outside", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := resolver(tt.name)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v want %#v", got, tt.want)
			}
		})
	}
	source := []byte(`local m=require("mod"); m.`)
	f := analysis.Parse(source)
	defer f.Close()
	f.ModuleMembers = resolver
	got, _ := f.KnownMembers([]string{"m"}, len(source))
	if len(got) != 2 {
		t.Fatalf("require completion: %+v", got)
	}
	server.docs[uri(filepath.Join(root, "mod.lua"))] = &document{text: []byte(`return {unsaved=1}`)}
	got = server.moduleResolver(uri(filepath.Join(root, "main.lua")))("mod")
	if !reflect.DeepEqual(got, []analysis.Member{{Name: "unsaved"}}) {
		t.Fatalf("unsaved exports: %+v", got)
	}
}

func TestEnvironmentModuleDefinitions(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, template, module string
		unsaved, forwarded     bool
	}{
		{name: "external source", template: "?.lua", module: "pkg/mod.lua"},
		{name: "directory module", template: "?/init.lua", module: "pkg/mod/init.lua"},
		{name: "prefixed template", template: "lib_?.lua", module: "lib_pkg/mod.lua"},
		{name: "unsaved dependency", template: "?.lua", module: "pkg/mod.lua", unsaved: true},
		{name: "forwarded dependency", template: "?.lua", module: "pkg/mod.lua", forwarded: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, library := t.TempDir(), t.TempDir()
			modulePath := filepath.Join(library, tt.module)
			if err := os.MkdirAll(filepath.Dir(modulePath), 0700); err != nil {
				t.Fatal(err)
			}
			source := "local M={}\nfunction M.run() end\nreturn M"
			disk := source
			if tt.unsaved {
				disk = "return {}"
			}
			if err := os.WriteFile(modulePath, []byte(disk), 0600); err != nil {
				t.Fatal(err)
			}
			name := "pkg.mod"
			if tt.forwarded {
				if err := os.WriteFile(filepath.Join(library, "forward.lua"), []byte(`return require("pkg.mod")`), 0600); err != nil {
					t.Fatal(err)
				}
				name = "forward"
			}
			text := []byte(`local m=require("` + name + `"); m.run()`)
			parsed := analysis.Parse(text)
			defer parsed.Close()
			uri := pathFileURI(filepath.Join(root, "main.lua"))
			server := &Server{
				workspaceRoots: []string{root},
				modulePaths:    []moduleSearchPath{{Root: root, Templates: []string{filepath.Join(library, tt.template)}}},
				docs:           map[string]*document{uri: {uri: uri, text: text, file: parsed}},
			}
			if tt.unsaved {
				server.docs[pathFileURI(modulePath)] = &document{text: []byte(source)}
			}
			got, err := server.definition(nil, &protocol.DefinitionParams{
				TextDocumentPositionParams: protocol.TextDocumentPositionParams{
					TextDocument: protocol.TextDocumentIdentifier{URI: uri},
					Position:     toPosition(parsed.PositionOf(strings.Index(string(text), "m.run") + 3)),
				},
			})
			location, ok := got.(protocol.Location)
			if err != nil || !ok || location.URI != pathFileURI(modulePath) || location.Range.Start.Line != 1 {
				t.Fatalf("definition=%+v err=%v, want %s line 1", got, err, modulePath)
			}
			members := server.moduleResolver(uri)(name)
			if len(members) != 1 || members[0].Name != "run" {
				t.Fatalf("completion members=%+v", members)
			}
		})
	}
}

func TestEnvironmentSearchOrderAndWorkspaceIsolation(t *testing.T) {
	t.Parallel()
	root, other, library := t.TempDir(), t.TempDir(), t.TempDir()
	for file, text := range map[string]string{
		filepath.Join(root, "same.lua"):    "return {project=1}",
		filepath.Join(other, "same.lua"):   "return {other=1}",
		filepath.Join(library, "same.lua"): "return {installed=1}",
	} {
		if err := os.WriteFile(file, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{
		docs: map[string]*document{}, workspaceRoots: []string{root, other},
		modulePaths: []moduleSearchPath{
			{Root: root, Templates: []string{filepath.Join(library, "?.lua"), "./?.lua"}},
			{Root: other, Templates: []string{"./?.lua"}},
		},
	}
	for _, tt := range []struct{ name, file, want string }{
		{name: "installed precedes local", file: filepath.Join(root, "main.lua"), want: "installed"},
		{name: "other workspace", file: filepath.Join(other, "main.lua"), want: "other"},
		{name: "opened dependency", file: filepath.Join(library, "forward.lua"), want: "installed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := server.moduleResolver(pathFileURI(tt.file))("same")
			if len(got) != 1 || got[0].Name != tt.want {
				t.Fatalf("got %+v want %s", got, tt.want)
			}
		})
	}
	// Rebuilding a resolver after an environment change must not reuse old exports.
	server.modulePaths[0].Templates = []string{"./?.lua"}
	got := server.moduleResolver(pathFileURI(filepath.Join(root, "main.lua")))("same")
	if len(got) != 1 || got[0].Name != "project" {
		t.Fatalf("stale environment: %+v", got)
	}
}

func TestUntrustedInitializationIgnoresModulePaths(t *testing.T) {
	t.Parallel()
	server := &Server{docs: map[string]*document{}}
	_, err := server.initialize(nil, &protocol.InitializeParams{InitializationOptions: map[string]any{
		"useInterpreter": false,
		"modulePaths":    []moduleSearchPath{{Root: "/project", Templates: []string{"/external/?.lua"}}},
	}})
	if err != nil || len(server.modulePaths) != 0 {
		t.Fatalf("untrusted paths accepted: %+v, %v", server.modulePaths, err)
	}
}

func TestModuleResolverDoesNotFollowOutsideSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.lua")
	if err := os.WriteFile(outside, []byte(`return {outside=1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.lua")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	server := &Server{docs: map[string]*document{}}
	path := filepath.ToSlash(filepath.Join(root, "main.lua"))
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	if got := server.moduleResolver(uri)("escape"); len(got) != 0 {
		t.Fatalf("outside export: %+v", got)
	}
	server.modulePaths = []moduleSearchPath{{Root: root, Templates: []string{filepath.Join(root, "?.lua")}}}
	if got := server.moduleResolver(uri)("escape"); len(got) != 0 {
		t.Fatalf("configured path followed an outside symlink: %+v", got)
	}
}

func TestModuleRootsRefreshBetweenRequests(t *testing.T) {
	root, library := t.TempDir(), filepath.Join(t.TempDir(), "packages")
	uri := pathFileURI(filepath.Join(root, "main.lua"))
	server := &Server{workspaceRoots: []string{root}, modulePaths: []moduleSearchPath{{
		Root: root, Templates: []string{filepath.Join(library, "?.lua"), filepath.Join(library, "?", "init.lua")},
	}}}
	if got := server.moduleResolver(uri)("installed"); len(got) != 0 {
		t.Fatalf("unexpected missing module: %+v", got)
	}
	if err := os.MkdirAll(library, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(library, "installed.lua"), []byte("return {available=true}"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := server.moduleResolver(uri)("installed"); len(got) != 1 || got[0].Name != "available" {
		t.Fatalf("new request retained missing root: %+v", got)
	}
}

func BenchmarkModuleSearchMisses(b *testing.B) {
	root := b.TempDir()
	directory := filepath.Join(root, "app", "features", "services", "handlers")
	if err := os.MkdirAll(directory, 0700); err != nil {
		b.Fatal(err)
	}
	server := &Server{workspaceRoots: []string{root}, modulePaths: []moduleSearchPath{{
		Root: root, Templates: []string{filepath.Join(root, "?.lua"), filepath.Join(root, "?", "init.lua")},
	}}}
	uri := pathFileURI(filepath.Join(directory, "main.lua"))
	b.ReportAllocs()
	for b.Loop() {
		resolve := server.moduleResolver(uri)
		for _, name := range []string{"missingA", "missingB", "missingC", "missingD"} {
			if got := resolve(name); len(got) != 0 {
				b.Fatal(got)
			}
		}
	}
}
