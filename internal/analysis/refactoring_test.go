package analysis

import (
	"strings"
	"testing"
)

func TestRenameBindings(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, source, next string
		count              int
		fails              bool
	}{
		{"closure", "local x=1; local function f() return x end; print(x)", "item", 3, false},
		{"shadow", "local x=1; do local x=2; print(x) end; print(x)", "item", 2, false},
		{"initializer", "local x=x; print(x)", "item", 2, false},
		{"capture outer use", "local x=1; print(y,x)", "y", 0, true},
		{"captured by inner", "local x=1; do local y=2; print(x,y) end", "y", 0, true},
		{"same scope", "local x=1; local y=2", "y", 0, true},
		{"keyword", "local x=1", "end", 0, true},
		{"environment", "local x=1", "_ENV", 0, true},
		{"global", "x=1; print(x)", "item", 0, true},
		{"comments and keys", "local x=1; -- x\nprint({x=x, text='x'})", "item", 2, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := Parse([]byte(tt.source))
			defer f.Close()
			spans, err := f.Rename(strings.Index(tt.source, "x"), tt.next)
			if (err != nil) != tt.fails || (!tt.fails && len(spans) != tt.count) {
				t.Fatalf("spans=%v error=%v", spans, err)
			}
		})
	}
}

func TestSignatureHelp(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, source, label string
		active              int
	}{
		{"local", "local function add(a,b) end; add(1, |)", "add(a, b)", 1},
		{"unfinished", "local function add(a,b) end; add(1, |", "add(a, b)", 1},
		{"nested commas", "local function add(a,b) end; add({1,2}, |)", "add(a, b)", 1},
		{"string commas", "local function add(a,b) end; add('x,y', |)", "add(a, b)", 1},
		{"alias", "local function add(a,b) end; local f=add; f(|)", "f(a, b)", 0},
		{"method", "local M={}; function M:run(a,b) end; M:run(1, |)", "M:run(a, b)", 1},
		{"method as function", "local M={}; function M:run(a,b) end; M.run(M, |)", "M.run(self, a, b)", 1},
		{"varargs", "local function f(a,...) end; f(1,2,3, |)", "f(a, ...)", 1},
		{"comment", "local function f(a) end; -- f(|)", "", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			at := strings.Index(tt.source, "|")
			f := Parse([]byte(strings.Replace(tt.source, "|", "", 1)))
			defer f.Close()
			got := f.SignatureAt(at)
			if tt.label == "" {
				if got != nil {
					t.Fatalf("unexpected %+v", got)
				}
				return
			}
			if got == nil || got.Label != tt.label || got.Active != tt.active {
				t.Fatalf("got %+v want %s param %d", got, tt.label, tt.active)
			}
		})
	}
}
