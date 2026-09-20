package analysis

import (
	"reflect"
	"testing"
)

func TestBustedTests(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, source string
		want         []string
	}{
		{"nested aliases", `describe("outer", function()
context('inner', function()
it('works (a+b)?', function() end)
spec('also works', function() end)
test('third', function() end)
end)
end)`, []string{"outer inner works (a+b)?", "outer inner also works", "outer inner third"}},
		{"top-level", `it("works", function() end)`, []string{"works"}},
		{"escapes", `describe('can\'t', function() it("a\nline",function()end) end)`, []string{"can't a\nline"}},
		{"long strings", `describe([=[outer]=],function() it([[inner]],function()end) end)`, []string{"outer inner"}},
		{"shadowed DSL", `local describe=function()end; describe('fake',function()it('no',function()end)end)`, nil},
		{"concatenated name", `it("a" .. "b",function()end)`, nil},
		{"empty name", `it("",function()end)`, []string{""}},
		{"dynamic group", `describe(name,function()it('no',function()end)end)`, nil},
		{"pending and helpers", `describe('suite',function() pending('later'); before_each(function()it('hidden',function()end)end) end)`, nil},
		{"same full name", `it('duplicate',function()end);it('duplicate',function()end)`, []string{}},
		{"different groups", `describe('a',function()it('same',function()end)end);describe('b',function()it('same',function()end)end)`, []string{"a same", "b same"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := Parse([]byte(tt.source))
			defer f.Close()
			found := f.BustedTests()
			var got []string
			if found != nil {
				got = []string{}
			}
			for _, test := range found {
				got = append(got, test.Name)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v want %#v", got, tt.want)
			}
		})
	}
}
