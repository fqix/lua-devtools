package lsp

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fqix/lua-devtools/internal/analysis"
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
}
