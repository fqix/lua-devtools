package lsp

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/fqix/lua-devtools/internal/analysis"
	"github.com/fqix/lua-devtools/internal/dap"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

// interpreter describes the selected runtime without loading user modules or
// LUA_INIT. A nil interpreter keeps the in-process Lua 5.4 fallback available.
type interpreter struct {
	path    string
	version string
	members map[string][]analysis.Member
	globals map[string]bool
}

const libraryProbe = `io.write(_VERSION, "\n")
for k,v in pairs(_G) do
 if type(k)=="string" then io.write("_G\t", k, "\t", type(v), "\n") end
end
for _,lib in ipairs{"bit","jit","bit32","coroutine","debug","io","math","os","package","string","table","utf8"} do
 for k,v in pairs(_G[lib] or {}) do
  if type(k)=="string" then io.write(lib, "\t", k, "\t", type(v), "\n") end
 end
end`

func inspectInterpreter(path string) *interpreter {
	if path == "" {
		path = dap.FindLuaInterpreter()
	}
	if path == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := interpreterCommand(ctx, path, libraryProbe).Output()
	if err != nil {
		return nil
	}
	// Windows Lua writes CRLF through its text-mode stdout.
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(output), "\r\n", "\n")), "\n")
	if len(lines) == 0 || (lines[0] != "Lua 5.1" && lines[0] != "Lua 5.2" && lines[0] != "Lua 5.3" && lines[0] != "Lua 5.4" && lines[0] != "Lua 5.5") {
		return nil
	}
	result := &interpreter{path: path, version: lines[0], members: map[string][]analysis.Member{}, globals: map[string]bool{}}
	for _, line := range lines[1:] {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "_G" {
			result.globals[parts[1]] = true
			continue
		}
		result.members[parts[0]] = append(result.members[parts[0]], analysis.Member{Name: parts[1], Function: parts[2] == "function"})
	}
	return result
}

// Lua 5.1 has no -E option. Remove Lua initialization and search-path overrides
// from the child environment for every version instead of executing startup code.
func interpreterCommand(ctx context.Context, path, probe string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, "-e", probe)
	cmd.Env = []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if key == "LUA_INIT" || strings.HasPrefix(key, "LUA_INIT_") ||
			key == "LUA_PATH" || strings.HasPrefix(key, "LUA_PATH_") ||
			key == "LUA_CPATH" || strings.HasPrefix(key, "LUA_CPATH_") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	return cmd
}

// Compile source without executing it. Reject binary chunks before loadstring
// on Lua 5.1, which has no text-only loading mode. Use a fixed diagnostic name.
const syntaxProbe = `local source=io.read("*a")
if source:sub(1,3)=="\239\187\191" then source=source:sub(4) end
if source:sub(1,1)=="#" then source=source:gsub("^[^\n]*", "", 1) end
if source:byte(1)==27 then io.write("document:1: binary chunks are not supported"); return end
local chunk,err
if _VERSION=="Lua 5.1" then
 chunk,err=loadstring(source,"@document")
else
 chunk,err=load(source,"@document","t",{})
end
if not chunk then io.write(err) end`

func (runtime *interpreter) syntaxDiagnostics(text []byte) ([]protocol.Diagnostic, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := interpreterCommand(ctx, runtime.path, syntaxProbe)
	cmd.Stdin = bytes.NewReader(text)
	output, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	diagnostics := []protocol.Diagnostic{}
	if len(output) == 0 {
		return diagnostics, true
	}
	parts := strings.SplitN(string(output), ":", 3)
	if len(parts) != 3 || parts[0] != "document" {
		return nil, false
	}
	line, err := strconv.Atoi(parts[1])
	if err != nil || line < 1 {
		return nil, false
	}
	lines := bytes.Split(text, []byte{'\n'})
	if line > len(lines) {
		line = len(lines)
	}
	start := protocol.Position{Line: uint32(line - 1)}
	end := protocol.Position{Line: uint32(line - 1), Character: uint32(len(utf16.Encode([]rune(string(lines[line-1])))))}
	severity := protocol.DiagnosticSeverityError
	source := Name + " (" + runtime.version + ")"
	diagnostics = append(diagnostics, protocol.Diagnostic{Range: protocol.Range{Start: start, End: end}, Severity: &severity, Source: &source, Message: strings.TrimSpace(parts[2])})
	return diagnostics, true
}
