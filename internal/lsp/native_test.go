package lsp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fqix/lua-devtools/internal/analysis"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestNativeModuleFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"lib/plain.so", "lib/pkg.so", "lib/pkg/sub.so"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	templates := []string{"lib/?.so", filepath.Join(root, "missing", "?.so")}
	for name, want := range map[string]string{
		"plain":     "lib/plain.so",
		"pkg.sub":   "lib/pkg/sub.so",
		"pkg.other": "lib/pkg.so", // all-in-one loader: luaopen_pkg_other inside pkg.so
		"absent":    "",
	} {
		got := nativeModuleFile(name, root, templates)
		if want != "" {
			want = filepath.Join(root, filepath.FromSlash(want))
		}
		if got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

// fakeInterpreter records every probe it receives and answers with fixed members.
func fakeInterpreter(t *testing.T, dir string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script interpreter")
	}
	log := filepath.Join(dir, "probes.log")
	script := filepath.Join(dir, "lua")
	body := "#!/bin/sh\nprintf '%s\\n%s\\n%s\\n---\\n' \"$2\" \"$LUA_PATH\" \"$LUA_CPATH\" >> \"" + log + "\"\n" +
		"case \"$2\" in *'\"pkg.b\"'*) printf 'second\\tfunction\\n';; *pcall*) printf 'encode\\tfunction\\nversion\\tstring\\n1\\tnumber\\n';; esac\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script, log
}

func TestNativeModuleMembersAreOptIn(t *testing.T) {
	root := t.TempDir()
	lua, log := fakeInterpreter(t, root)
	library := filepath.Join(root, "lib")
	if err := os.MkdirAll(library, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(library, "cjson.so"), []byte("not a real library"), 0o600); err != nil {
		t.Fatal(err)
	}
	uri := pathFileURI(filepath.Join(root, "main.lua"))
	text := []byte(`local json = require("cjson"); json.`)
	file := analysis.Parse(text)
	defer file.Close()
	environment := moduleSearchPath{Root: root, Templates: []string{filepath.Join(root, "?.lua")}, CTemplates: []string{"lib/?.so"}}
	server := &Server{
		runtime:        &interpreter{path: lua, version: "Lua 5.4", members: map[string][]analysis.Member{}, globals: map[string]bool{}},
		workspaceRoots: []string{root},
		modulePaths:    []moduleSearchPath{environment},
		docs:           map[string]*document{uri: {uri: uri, text: text, file: file}},
	}
	complete := func() []string {
		result, err := server.completion(nil, &protocol.CompletionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: toPosition(file.PositionOf(len(text))),
		}})
		if err != nil {
			t.Fatal(err)
		}
		labels := []string{}
		for _, item := range result.(protocol.CompletionList).Items {
			labels = append(labels, item.Label)
		}
		return labels
	}
	if got := complete(); len(got) != 0 {
		t.Fatalf("native members offered while the setting is off: %v", got)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("interpreter ran while the setting is off")
	}
	server.probeNative = true
	if got := strings.Join(complete(), ","); got != "encode,version" {
		t.Fatalf("native members = %q", got)
	}
	probes, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(probes), `pcall(require, "cjson")`) {
		t.Fatalf("probe = %s", probes)
	}
	if !strings.Contains(string(probes), filepath.Join(root, "lib", "?.so")+";;") || !strings.Contains(string(probes), filepath.Join(root, "?.lua")+";;") {
		t.Fatalf("search paths not passed: %s", probes)
	}
	// A second completion is served from the cache without loading the module again.
	complete()
	if again, _ := os.ReadFile(log); strings.Count(string(again), "---") != 1 {
		t.Fatalf("module probed again: %s", again)
	}
	// Without a shared library for the name, nothing is executed.
	server.docs[uri].text = []byte(`local m = require("nothing"); m.`)
	server.docs[uri].file = analysis.Parse(server.docs[uri].text)
	defer server.docs[uri].file.Close()
	file = server.docs[uri].file
	text = server.docs[uri].text
	if got := complete(); len(got) != 0 {
		t.Fatalf("members for a missing module: %v", got)
	}
	if again, _ := os.ReadFile(log); strings.Count(string(again), "---") != 1 {
		t.Fatalf("interpreter ran for a module without a library: %s", again)
	}
}

func TestNativeCacheIsPerModule(t *testing.T) {
	root := t.TempDir()
	lua, _ := fakeInterpreter(t, root)
	if err := os.WriteFile(filepath.Join(root, "pkg.so"), []byte("all-in-one"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{runtime: &interpreter{path: lua, version: "Lua 5.4"}, probeNative: true}
	environment := &moduleSearchPath{Root: root, CTemplates: []string{"?.so"}}
	first := server.nativeModuleMembers("pkg.a", root, environment)
	second := server.nativeModuleMembers("pkg.b", root, environment)
	if len(first) != 2 || first[0].Name != "encode" || len(second) != 1 || second[0].Name != "second" {
		t.Fatalf("pkg.a = %+v, pkg.b = %+v", first, second)
	}
}
