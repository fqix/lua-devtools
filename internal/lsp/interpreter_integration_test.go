//go:build integration

package lsp

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fqix/lua-devtools/internal/analysis"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestInterpreterLanguageVersion(t *testing.T) {
	path := os.Getenv("LUA_TEST_BINARY")
	runtime := inspectInterpreter(path)
	if runtime == nil {
		if path != "" {
			t.Fatalf("cannot inspect selected interpreter %q", path)
		}
		t.Skip("Lua interpreter unavailable")
	}
	if expected := os.Getenv("LUA_TEST_VERSION"); expected != "" && !strings.HasPrefix(expected, strings.TrimPrefix(runtime.version, "Lua ")+".") {
		t.Fatalf("version=%s, want=%s", runtime.version, expected)
	}
	t.Log(runtime.version)
	if runtime.version == "Lua 5.1" {
		for _, name := range []string{"loadstring", "setfenv", "getfenv", "unpack"} {
			if !runtime.globals[name] {
				t.Errorf("missing Lua 5.1 global %s", name)
			}
		}
		if runtime.globals["utf8"] || runtime.globals["warn"] {
			t.Error("unexpected modern Lua globals")
		}
	}
	for _, tt := range []struct {
		source string
		valid  bool
	}{
		{`local value = 1`, true},
		{"\x1bLua\x51invalid", false},
		{`local value =`, false},
		{`return 7 // 2`, (runtime.version != "Lua 5.1" && runtime.version != "Lua 5.2")},
		{`return 3 & 1`, runtime.globals["jit"] || (runtime.version != "Lua 5.1" && runtime.version != "Lua 5.2")},
		{`local x <const> = 1`, (runtime.version == "Lua 5.4" || runtime.version == "Lua 5.5")},
		{`global x; x=1`, runtime.version == "Lua 5.5"},
		{`global <const> a,b=1,2; global function f(...args) local <const> value=args.n+a; return value end`, runtime.version == "Lua 5.5"},
		{`global <const> *; local <const> value=math.pi`, runtime.version == "Lua 5.5"},
		{"#!/usr/bin/env lua\nlocal x = 1", true},
	} {
		t.Run(tt.source, func(t *testing.T) {
			diagnostics, ok := runtime.syntaxDiagnostics([]byte(tt.source))
			if !ok || (len(diagnostics) == 0) != tt.valid {
				t.Fatalf("%q: %+v, ok=%v", tt.source, diagnostics, ok)
			}
			if !tt.valid && (diagnostics[0].Source == nil || !strings.Contains(*diagnostics[0].Source, runtime.version)) {
				t.Errorf("missing interpreter version in diagnostic: %+v", diagnostics[0])
			}
		})
	}
	has := func(lib, name string) bool {
		for _, v := range runtime.members[lib] {
			if v.Name == name {
				return true
			}
		}
		return false
	}
	if has("coroutine", "close") != (runtime.version == "Lua 5.4" || runtime.version == "Lua 5.5") {
		t.Error("coroutine.close version mismatch")
	}
	if has("table", "create") != (runtime.version == "Lua 5.5") {
		t.Error("table.create version mismatch")
	}
	if has("string", "pack") != (runtime.version != "Lua 5.1" && runtime.version != "Lua 5.2") {
		t.Error("string.pack version mismatch")
	}
	if runtime.version == "Lua 5.2" && (!has("bit32", "band") || runtime.globals["utf8"] || runtime.globals["warn"]) {
		t.Error("Lua 5.2 standard libraries mismatch")
	}
	f := analysis.Parse([]byte("table."))
	defer f.Close()
	items := memberCompletions(f, len(f.Text), []string{"table"}, false, runtime).Items
	found := false
	for _, item := range items {
		if item.Label == "create" {
			found = true
		}
	}
	if found != (runtime.version == "Lua 5.5") {
		t.Error("completion version mismatch")
	}
	for _, tt := range []struct {
		source, label string
		want          bool
	}{
		{"", "bit32", runtime.globals["bit32"]},
		{"", "loadstring", runtime.globals["loadstring"]},
		{"", "unpack", runtime.globals["unpack"]},
		{"", "setfenv", runtime.globals["setfenv"]},
		{"", "getfenv", runtime.globals["getfenv"]},
		{"", "module", runtime.globals["module"]},
		{"", "newproxy", runtime.globals["newproxy"]},
		{"", "bit", runtime.globals["bit"]},
		{"", "jit", runtime.globals["jit"]},
		{"bit.", "band", runtime.globals["bit"]},
		{"jit.", "on", runtime.globals["jit"]},
		{"", "utf8", runtime.globals["utf8"]},
		{"bit32.", "band", has("bit32", "band")},
		{"string.", "pack", (runtime.version != "Lua 5.1" && runtime.version != "Lua 5.2")},
		{"table.", "move", has("table", "move")},
		{"math.", "maxinteger", (runtime.version != "Lua 5.1" && runtime.version != "Lua 5.2")},
	} {
		t.Run(tt.source+tt.label, func(t *testing.T) {
			f := analysis.Parse([]byte(tt.source))
			defer f.Close()
			s := &Server{runtime: runtime, docs: map[string]*document{"test": {file: f}}}
			result, err := s.completion(nil, &protocol.CompletionParams{
				TextDocumentPositionParams: protocol.TextDocumentPositionParams{
					TextDocument: protocol.TextDocumentIdentifier{URI: "test"},
					Position:     toPosition(f.PositionOf(len(f.Text))),
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			found := slices.ContainsFunc(result.(protocol.CompletionList).Items, func(item protocol.CompletionItem) bool { return item.Label == tt.label })
			if found != tt.want {
				t.Errorf("completion %q present=%v, want=%v", tt.label, found, tt.want)
			}
		})
	}
}

func TestInterpreterDoesNotExecuteDocumentOrInit(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed")
	init := `local f=assert(io.open(` + "\"" + filepath.ToSlash(marker) + "\"" + `,"w")); f:write("executed"); f:close()`
	t.Setenv("LUA_INIT", init)
	for _, version := range []string{"5_1", "5_2", "5_3", "5_4", "5_5"} {
		t.Setenv("LUA_INIT_"+version, init)
	}
	runtime := inspectInterpreter(os.Getenv("LUA_TEST_BINARY"))
	if runtime == nil {
		if os.Getenv("LUA_TEST_BINARY") != "" {
			t.Fatal("interpreter unavailable")
		}
		t.Skip("Lua unavailable")
	}
	if diagnostics, ok := runtime.syntaxDiagnostics([]byte(init)); !ok || len(diagnostics) != 0 {
		t.Fatalf("diagnostics: %+v, ok=%v", diagnostics, ok)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("document or LUA_INIT executed: %v", err)
	}
}

// A file that is not a loadable library yields no members and no error; the
// interpreter reports the failure and the result is cached.
func TestNativeProbeUnloadableLibrary(t *testing.T) {
	runtime := inspectInterpreter(os.Getenv("LUA_TEST_BINARY"))
	if runtime == nil {
		t.Skip("no Lua interpreter")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken.so"), []byte("not a shared library"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{runtime: runtime, probeNative: true}
	environment := &moduleSearchPath{Root: root, CTemplates: []string{"?.so"}}
	if members := server.nativeModuleMembers("broken", root, environment); members != nil {
		t.Fatalf("members = %+v", members)
	}
	if entry, ok := server.nativeCache[filepath.Join(root, "broken.so")+"\x00broken"]; !ok || entry.members != nil {
		t.Fatalf("failure not cached: %+v", server.nativeCache)
	}
}
