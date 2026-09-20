package analysis

import (
	"strings"
	"testing"
)

func TestLua55Declarations(t *testing.T) {
	tests := []struct {
		name, source, use, declaration string
		global                         bool
	}{
		{"global shadows local", "local x=1; do global x; x=2 end; return x", "x=2", "x;", true},
		{"local restored outside block", "local x=1; do global x; x=2 end; return x", "x", "x=1", false},
		{"global initializer sees outer", "local x=1; do global x=x+1 end", "x+1", "x=1", false},
		{"global recursive function", "local f; do global function f() return f() end end", "f() end", "f() return", true},
		{"nested closure", "global x; local function f() return x end", "x end", "x;", true},
		{"local shadows global", "global x; do local x=1; x=x+1 end", "x+1", "x=1", false},
		{"nested global declaration", "global x; do global x; x=1 end", "x=1", "x; x=1", true},
		{"prefixed global attribute", "global <const> x, y=1,2; return y", "y", "y=1", true},
		{"postfixed global attribute", "global x<const> = 1; return x", "x", "x<const>", true},
		{"prefixed local attribute", "local <const> x,y=1,2; return y", "y", "y=1", false},
		{"named vararg", "local function f(a, ...args) return args[1] end", "args[1]", "args)", false},
		{"named vararg closure", "local f=function(...args) return function() return args.n end end", "args.n", "args)", false},
		{"repeat global", "repeat global x; x=1 until x>0", "x>0", "x;", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Parse([]byte(tt.source))
			defer f.Close()
			if len(f.Diagnostics) > 0 {
				t.Fatalf("diagnostics: %+v", f.Diagnostics)
			}
			sym, _ := f.SymbolAt(strings.LastIndex(tt.source, tt.use))
			if sym == nil || sym.Start != strings.Index(tt.source, tt.declaration) || sym.Global != tt.global {
				t.Fatalf("definition=%+v, want offset=%d global=%v", sym, strings.Index(tt.source, tt.declaration), tt.global)
			}
		})
	}
}

func TestLua55GlobalVisibility(t *testing.T) {
	tests := []struct {
		name, source, use string
		resolved          bool
	}{
		{"block declaration does not leak", "do global x; x=1 end; return x", "x", false},
		{"declaration not hoisted", "local before=x; global x", "x;", false},
		{"implicit default disabled", "x=1; do global y; local z=x end", "x end", false},
		{"explicit wildcard retained", "x=1; global *; do global y; local z=x end", "x end", true},
		{"wildcard reopens globals", "x=1; do global y; global *; local z=x end", "x end", true},
		{"wildcard does not escape block", "x=1; global y; do global * end; local z=x", "x", false},
		{"const wildcard", "x=1; global <const> *; return x", "x", true},
		{"local wins over wildcard", "local x=1; global *; return x", "x", true},
		{"undeclared assignment is not definition", "global y; x=1; return x", "x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Parse([]byte(tt.source))
			defer f.Close()
			if len(f.Diagnostics) > 0 {
				t.Fatalf("diagnostics: %+v", f.Diagnostics)
			}
			at := strings.LastIndex(tt.source, tt.use)
			sym, _ := f.SymbolAt(at)
			if (sym != nil) != tt.resolved {
				t.Fatalf("symbol=%+v, want resolved=%v", sym, tt.resolved)
			}
			visible := false
			for _, s := range f.VisibleSymbols(at) {
				if s.Name == "x" {
					visible = true
				}
			}
			if visible != tt.resolved {
				t.Fatalf("visible x=%v, want %v", visible, tt.resolved)
			}
		})
	}
}

func TestLua55Outline(t *testing.T) {
	source := "global <const> a,b=1,2\nglobal function f(...args)\n global inner\n local <const> value=args.n\n return value\nend\nglobal *\n"
	f := Parse([]byte(source))
	defer f.Close()
	if len(f.Diagnostics) > 0 {
		t.Fatalf("diagnostics: %+v", f.Diagnostics)
	}
	symbols := f.Outline()
	if len(symbols) != 3 {
		t.Fatalf("outline=%+v", symbols)
	}
	for i, name := range []string{"a", "b", "f"} {
		if symbols[i].Name != name || !symbols[i].Global {
			t.Fatalf("symbol=%+v", symbols[i])
		}
	}
	fn := symbols[2]
	if fn.Kind != KindFunction || fn.Detail != "global function f(...args)" || fn.FullStart != strings.Index(source, "global function") || fn.FullEnd != strings.Index(source, "\nend")+4 {
		t.Fatalf("function=%+v", fn)
	}
	children := f.OutlineChildren(fn)
	if len(children) != 2 || children[0].Name != "inner" || children[1].Name != "value" {
		t.Fatalf("children=%+v", children)
	}
}

func TestLua55GlobalTableMembers(t *testing.T) {
	source := "global t={field=1}; local result=t.field"
	f := Parse([]byte(source))
	defer f.Close()
	members, declared := f.KnownMembers([]string{"t"}, len(source))
	if !declared || len(members) != 1 || members[0].Name != "field" {
		t.Fatalf("members=%+v declared=%v", members, declared)
	}
}
