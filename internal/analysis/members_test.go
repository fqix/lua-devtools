package analysis

import (
	"reflect"
	"strings"
	"testing"
)

func TestKnownMembers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		source   string
		receiver []string
		want     []Member
		declared bool
	}{
		{
			name: "literal and nested fields", source: `local m = {x = 1, run = function() end, nested = {value = 2}}; m.`,
			receiver: []string{"m"}, declared: true,
			want: []Member{{Name: "nested"}, {Name: "run", Function: true}, {Name: "x"}},
		},
		{
			name: "nested receiver", source: `local m = {nested = {value = 2}}; m.nested.`,
			receiver: []string{"m", "nested"}, declared: true, want: []Member{{Name: "value"}},
		},
		{
			name: "assignments and methods", source: "local m = {}\nm.x = 1\nfunction m:run() end\nm.",
			receiver: []string{"m"}, declared: true, want: []Member{{Name: "run", Function: true}, {Name: "x"}},
		},
		{
			name: "shadowed local", source: "local m = {outer = 1}\ndo\nlocal m = {inner = 1}\nm.|\nend",
			receiver: []string{"m"}, declared: true, want: []Member{{Name: "inner"}},
		},
		{
			name: "parameter shadows library", source: "local function f(math)\nmath.|abs()\nend",
			receiver: []string{"math"}, declared: true, want: []Member{},
		},
		{
			name: "future field excluded", source: "local m = {}\nm.|\nm.later = 1",
			receiver: []string{"m"}, declared: true, want: []Member{},
		},
		{
			name: "reassignment replaces shape", source: "local m = {old = 1}\nm = {new = 2}\nm.",
			receiver: []string{"m"}, declared: true, want: []Member{{Name: "new"}},
		},
		{
			name: "scalar clears shape", source: "local m = {old = 1}\nm = 0\nm.",
			receiver: []string{"m"}, declared: true, want: []Member{},
		},
		{
			name: "plain string keys only", source: `local key = "computed"; local m = { [key] = 1, ["valid"] = 2, ["bad-key"] = 3 }; m.`,
			receiver: []string{"m"}, declared: true, want: []Member{{Name: "valid"}},
		},
		{
			name: "unknown library", source: "math.", receiver: []string{"math"}, want: []Member{},
		},
		{
			name: "sibling function writes excluded", source: "local m = {}\nfunction setup() m.hidden = 1 end\nm.",
			receiver: []string{"m"}, declared: true, want: []Member{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			offset := strings.IndexByte(tt.source, '|')
			source := strings.Replace(tt.source, "|", "", 1)
			if offset < 0 {
				offset = len(source)
			}
			f := Parse([]byte(source))
			defer f.Close()
			got, declared := f.KnownMembers(tt.receiver, offset)
			if !reflect.DeepEqual(got, tt.want) || declared != tt.declared {
				t.Fatalf("members = %#v, declared = %v; want %#v, %v", got, declared, tt.want, tt.declared)
			}
		})
	}
}

func TestInferredMembers(t *testing.T) {
	tests := []struct {
		name, source string
		want         []Member
	}{
		{"simultaneous target capture", `local a={x=1}; local b=a; a,a.y={},2; a.`, []Member{}},
		{"simultaneous swap", `local a={x=1}; local b={y=2}; a,b=b,a; a.`, []Member{{Name: "y"}}},
		{"alias writes", `local a = {x=1}; local b=a; b.y=2; a.`, []Member{{Name: "x"}, {Name: "y"}}},
		{"alias rebinding", `local a={x=1}; local b=a; b={y=2}; a.`, []Member{{Name: "x"}}},
		{"old alias survives", `local b={x=1}; local a=b; b={y=2}; a.`, []Member{{Name: "x"}}},
		{"nested alias", `local b={nested={x=1}}; local a=b.nested; a.`, []Member{{Name: "x"}}},
		{"remove field", `local a={x=1,y=2}; a.x=nil; a.`, []Member{{Name: "y"}}},
		{"metatable", `local base={run=function() end}; local a=setmetatable({x=1},{__index=base}); a.`, []Member{{Name: "run", Function: true}, {Name: "x"}}},
		{"metatable cycle", `local a={x=1}; local b=setmetatable(a,{__index=a}); a.`, []Member{{Name: "x"}}},
		{"shadowed setmetatable", `local setmetatable=function() end; local a=setmetatable({x=1},{}); a.`, []Member{}},
		{"literal return", `local function make() return {run=function() end,x=1} end; local a=make(); a.`, []Member{{Name: "run", Function: true}, {Name: "x"}}},
		{"anonymous return", `local make=function() return {x=1} end; local a=make(); a.`, []Member{{Name: "x"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Parse([]byte(tt.source))
			defer f.Close()
			got, _ := f.KnownMembers([]string{"a"}, len(tt.source))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}
