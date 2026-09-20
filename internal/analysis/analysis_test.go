package analysis

import (
	"strings"
	"testing"
)

const sample = `local function add(a, b)
    local result = a + b
    return result
end

local x = 10
local y = 20
local total = add(x, y)
print("total:", total)

function greet(name)
    counter = (counter or 0) + 1
    return "hi " .. name
end

local M = {}
function M.run(n)
    for i = 1, n do
        local sq = i * i
    end
    for k, v in pairs(M) do end
end
`

// offsetOf finds the byte offset of the nth occurrence of needle.
func offsetOf(t *testing.T, text, needle string, nth int) int {
	t.Helper()
	from := 0
	for i := 0; i <= nth; i++ {
		idx := strings.Index(text[from:], needle)
		if idx < 0 {
			t.Fatalf("occurrence %d of %q not found", nth, needle)
		}
		from += idx
		if i < nth {
			from += len(needle)
		}
	}
	return from
}

func TestResolveReferences(t *testing.T) {
	f := Parse([]byte(sample))
	defer f.Close()
	if len(f.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", f.Diagnostics)
	}

	cases := []struct {
		needle string
		nth    int
		kind   SymbolKind
		declAt string
	}{
		{"a + b", 0, KindParameter, "a, b"},         // `a` in the body resolves to the parameter
		{"result\n", 0, KindLocal, "result ="},      // `return result` -> local
		{"add(x, y)", 0, KindFunction, "add(a, b)"}, // call resolves to local function
		{"total)", 0, KindLocal, "total ="},
		{"name\n", 0, KindParameter, "name)"},
		{"counter or", 0, KindGlobal, "counter ="}, // global defined by its first assignment
		{"i * i", 0, KindLocal, "i = 1"},
		{"M) do", 0, KindLocal, "M = {}"},
	}
	for _, c := range cases {
		off := offsetOf(t, sample, c.needle, c.nth)
		sym, ref := f.SymbolAt(off)
		if ref == nil {
			t.Errorf("%q: no reference at offset %d", c.needle, off)
			continue
		}
		if sym == nil {
			t.Errorf("%q: unresolved", c.needle)
			continue
		}
		if sym.Kind != c.kind {
			t.Errorf("%q: kind = %s, want %s", c.needle, sym.Kind, c.kind)
		}
		if want := offsetOf(t, sample, c.declAt, 0); sym.Start != want {
			t.Errorf("%q: declared at %d, want %d (%q)", c.needle, sym.Start, want, c.declAt)
		}
	}

	// `print` and `pairs` are unknown globals: referenced but unresolved.
	if sym, ref := f.SymbolAt(offsetOf(t, sample, "print(", 0)); ref == nil || sym != nil {
		t.Errorf("print: ref=%v sym=%v, want unresolved reference", ref, sym)
	}
}

func TestLocalNotVisibleInOwnInitializer(t *testing.T) {
	src := "local x = 1\ndo\n  local x = x + 1\nend\n"
	f := Parse([]byte(src))
	defer f.Close()
	inner := strings.LastIndex(src, "x + 1")
	sym, _ := f.SymbolAt(inner)
	if sym == nil || sym.Start != strings.Index(src, "x = 1\n") {
		t.Fatalf("inner x should resolve to the outer local, got %+v", sym)
	}
}

func TestRepeatUntilSeesBodyLocals(t *testing.T) {
	src := "local x = 10\nrepeat\n  local x = 1\nuntil x > 0\n"
	f := Parse([]byte(src))
	defer f.Close()
	sym, _ := f.SymbolAt(strings.Index(src, "x > 0"))
	if sym == nil || sym.Start != strings.Index(src, "x = 1\n") {
		t.Fatalf("until condition should see the body local, got %+v", sym)
	}
}

func TestComputedTableKeyIsReference(t *testing.T) {
	src := "local x = 'k'\nlocal t = { [x] = 3, x = 4 }\n"
	f := Parse([]byte(src))
	defer f.Close()
	if sym, ref := f.SymbolAt(strings.Index(src, "[x]") + 1); ref == nil || sym == nil || sym.Start != strings.Index(src, "x = 'k'") {
		t.Fatalf("[x] should reference the local x, got sym=%+v ref=%+v", sym, ref)
	}
	if _, ref := f.SymbolAt(strings.Index(src, " x = 4") + 1); ref != nil {
		t.Fatalf("literal key x should not be a reference, got %+v", ref)
	}
}

func TestVisibleSymbolsAndOutline(t *testing.T) {
	f := Parse([]byte(sample))
	defer f.Close()

	names := func(syms []*Symbol) []string {
		var out []string
		for _, s := range syms {
			out = append(out, s.Name)
		}
		return out
	}

	inside := offsetOf(t, sample, "return result", 0)
	got := strings.Join(names(f.VisibleSymbols(inside)), ",")
	// innermost first: result, then parameters, then the function itself, then globals
	if !strings.HasPrefix(got, "result,b,a,add") || !strings.Contains(got, "counter") {
		t.Errorf("visible symbols = %s", got)
	}
	// x, y, total are declared later and must not be visible inside add.
	if strings.Contains(got, "total") {
		t.Errorf("total should not be visible inside add: %s", got)
	}

	top := names(f.Outline())
	if strings.Join(top, ",") != "add,x,y,total,greet,M,M.run" {
		t.Errorf("outline = %v", top)
	}
	for _, s := range f.Outline() {
		if s.Name == "add" {
			if kids := names(f.OutlineChildren(s)); strings.Join(kids, ",") != "result" {
				t.Errorf("add children = %v", kids)
			}
			if s.Detail != "function add(a, b)" {
				t.Errorf("add detail = %q", s.Detail)
			}
		}
	}
}

func TestDiagnostics(t *testing.T) {
	f := Parse([]byte("local x = \nprint(x\n"))
	defer f.Close()
	if len(f.Diagnostics) == 0 {
		t.Fatal("expected a syntax error")
	}
	if k := f.Diagnostics[0].Key; k != "diagnostic.missing" && k != "diagnostic.unexpected" {
		t.Errorf("key = %q", k)
	}
}

func TestPositions(t *testing.T) {
	src := "ab\n日本x\nz"
	f := Parse([]byte(src))
	defer f.Close()
	off := strings.Index(src, "x")
	pos := f.PositionOf(off)
	if pos != (Position{Line: 1, Character: 2}) {
		t.Errorf("PositionOf = %+v", pos)
	}
	if back := f.OffsetOf(pos); back != off {
		t.Errorf("OffsetOf = %d, want %d", back, off)
	}
	if f.OffsetOf(Position{Line: 5, Character: 0}) != len(src) {
		t.Error("out-of-range line should clamp")
	}
}
