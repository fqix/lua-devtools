//go:build integration

package dap

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRuntimeTableSnapshot(t *testing.T) {
	r, file := startTestRuntime(t, `local shared = {ok=true}
local value = {text="中文\n\"hello\"", precise=1.2345678901234567, list={1,false,3}, empty={}, a=shared,b=shared}
for i=1,250 do value["key"..i]=i end
setmetatable(value,{__pairs=function() error("pairs invoked") end,__index=function() error("index invoked") end,__len=function() error("len invoked") end})
local cyclic={}; cyclic.self=cyclic
local mixed={[1]=1, name="test"}
value.callback=function() error("snapshot executed function") end
local binary={text=string.char(255)}
local infinite={n=math.huge}
local huge={s=string.rep("x",600000)}
local deep={}; local current=deep; for i=1,34 do current.child={}; current=current.child end
local large={}; for i=1,10001 do large[i]=i end
local sparse={[2]="two"}
local stop=1
stop=2
`)
	runtimeRequest(t, func() error { _, err := r.SetBreakpoints(file, []SourceBreakpoint{{Line: 14}}); return err })
	runtimeRequest(t, func() error { return r.Resume("run", nil) })
	runtimeEvent(t, r, "stopped")
	var ref int
	runtimeRequest(t, func() error { v, err := r.Evaluate("value", 1); ref = v.VariablesReference; return err })
	var result snapshotInfo
	runtimeRequest(t, func() error { var err error; result, err = r.snapshot(ref); return err })
	var value map[string]any
	if err := json.Unmarshal([]byte(result.JSON), &value); err != nil {
		t.Fatal(err)
	}
	if value["callback"] != "<function>" {
		t.Fatalf("function marker missing: %s", result.JSON)
	}
	if value["key250"] != float64(250) || value["precise"] != 1.2345678901234567 || value["text"] != "中文\n\"hello\"" {
		t.Fatalf("snapshot lost values: %s", result.JSON)
	}
	if !strings.Contains(result.JSON, `"empty":{}`) || !strings.Contains(result.JSON, `"list":[1,false,3]`) || !strings.Contains(result.JSON, `"b":{"ok":true}`) {
		t.Fatalf("snapshot shape: %s", result.JSON)
	}
	for _, tt := range []struct{ name, message string }{
		{"huge", "512 KiB"}, {"deep", "32 levels"}, {"large", "10000"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runtimeRequest(t, func() error {
				v, err := r.Evaluate(tt.name, 1)
				if err != nil {
					return err
				}
				_, err = r.snapshot(v.VariablesReference)
				if err == nil || !strings.Contains(err.Error(), tt.message) {
					t.Errorf("error=%v, want %s", err, tt.message)
				}
				return nil
			})
		})
	}
	for _, name := range []string{"cyclic", "mixed", "binary", "infinite", "sparse"} {
		t.Run(name, func(t *testing.T) {
			runtimeRequest(t, func() error {
				v, err := r.Evaluate(name, 1)
				if err != nil {
					return err
				}
				snapshot, err := r.snapshot(v.VariablesReference)
				if err != nil {
					return err
				}
				var tagged map[string]any
				if err := json.Unmarshal([]byte(snapshot.JSON), &tagged); err != nil {
					return err
				}
				if tagged["$format"] != "lua-table-v1" {
					t.Fatalf("missing tagged snapshot: %s", snapshot.JSON)
				}
				return nil
			})
		})
	}

	runtimeRequest(t, func() error { return r.Resume("next", nil) })
	runtimeEvent(t, r, "stopped")
	runtimeRequest(t, func() error { _, err := r.Evaluate("value", 1); return err })
	runtimeRequest(t, func() error {
		_, err := r.snapshot(ref)
		if err == nil {
			t.Error("stale reference accepted")
		}
		return nil
	})
}

// The same codec serves protocol messages and strict snapshots without leaking options.
func TestJSONEncodingPolicies(t *testing.T) {
	lua := os.Getenv("LUA_TEST_BINARY")
	if lua == "" {
		lua = FindLuaInterpreter()
	}
	if lua == "" {
		t.Skip("Lua interpreter is not installed")
	}
	output, err := exec.CommandContext(t.Context(), lua, "-e", `
local json = dofile("../../vscode/lua/json.lua")
local snapshot = assert(loadfile("../../vscode/lua/snapshot.lua"))(json.encode)
assert(json.encode({}) == "[]")
assert(snapshot({}) == "{}")
assert(json.encode({}) == "[]")
local n = 1.2345678901234567
assert(json.encode(n) == string.format("%.14g", n))
assert(snapshot({n}) == "[" .. string.format("%.17g", n) .. "]")
if math.maxinteger then assert(snapshot({math.maxinteger}) == "[" .. tostring(math.maxinteger) .. "]") end
local text = string.char(0, 1, 8, 9, 10, 12, 13, 31, 34, 92) .. "中文"
assert(json.decode(json.encode(text)) == text)
assert(json.decode(snapshot({text}))[1] == text)
local binary = string.char(255)
assert(json.encode(binary) == '"' .. binary .. '"')
local bytes = json.decode(snapshot({binary}))
assert(bytes.root.entries[1].value.hex == "ff")
local cycle = {}; cycle.self = cycle; cycle[cycle] = "table key"
local tagged = json.decode(snapshot(cycle))
assert(tagged.root["$id"] == 1 and tagged["$format"] == "lua-table-v1")
local refs = 0
for _, entry in ipairs(tagged.root.entries) do
 if type(entry.key)=="table" and entry.key["$ref"]==1 then refs=refs+1 end
 if type(entry.value)=="table" and entry.value["$ref"]==1 then refs=refs+1 end
end
assert(refs == 2)
local reserved = { ["$ref"]="literal", [4]=math.huge }
assert(json.decode(snapshot(reserved)).root.entries[1].value.value == "+Infinity")
assert(json.encode(binary) == '"' .. binary .. '"')
local function trap() error("metamethod invoked") end
local thread = coroutine.create(trap)
local marked = json.decode(snapshot({fn=trap, nested={trap, thread, io.stdout}, keep=42}))
assert(marked.fn == "<function>" and marked.keep == 42)
assert(marked.nested[1] == "<function>" and marked.nested[2] == "<thread>" and marked.nested[3] == "<userdata>")
assert(not pcall(json.encode, {trap}))
if newproxy then
  local proxy = newproxy(true)
  getmetatable(proxy).__tostring = trap
  assert(snapshot({proxy}) == '["<userdata>"]')
end
local raw = setmetatable({10, 20}, {__pairs=trap, __index=trap, __len=trap, __tostring=trap})
assert(json.encode(raw) == "[10,20]")
assert(snapshot(raw) == "[10,20]")
assert(snapshot({z=1,a=2}) == '{"a":2,"z":1}')
assert(not pcall(json.encode, {string.rep("x", 10)}, {maxBytes=8}))
assert(json.encode({string.rep("x", 10)}) == '["xxxxxxxxxx"]')
`).CombinedOutput()
	if err != nil {
		t.Fatalf("JSON encoding policies: %v: %s", err, output)
	}
}
