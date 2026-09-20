package analysis

import (
	"reflect"
	"strings"
	"testing"
)

func TestLuaUnitTests(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, source string
		want         []string
	}{
		{"methods and globals", `local lu=require('luaunit')
TestFoo={}
function TestFoo:testBar() end
function TestFoo.testDot() end
function testGlobal() end
function TestFoo:setUp() end
os.exit(lu.LuaUnit.run())`, []string{"TestFoo.testBar", "TestFoo.testDot", "testGlobal"}},
		{"assignments and literal", `require "luaunit"
TestFoo={testLiteral=function() end, ['testQuoted']=function() end}
TestFoo.testAssigned=function() end
testGlobal=function() end`, []string{"TestFoo.testLiteral", "TestFoo.testQuoted", "TestFoo.testAssigned", "testGlobal"}},
		{"locals and nested excluded", `require 'luaunit'
local TestLocal={}
function TestLocal:testHidden() end
local function testHidden() end
function helper() function testNested() end end
function ordinary() end`, []string{}},
		{"no import", `function testOrdinary() end`, nil},
		{"comments and strings", `-- require('luaunit')
local text="require('luaunit')"
function testOrdinary() end`, nil},
		{"shadowed require", `local require=function() end
require('luaunit')
function testOrdinary() end`, nil},
		{"nested import", `function helper() require('luaunit') end
function testOrdinary() end`, nil},
		{"replaced definitions", `require('luaunit')
TestFoo={testOld=function() end}
TestFoo={}
function TestFoo:testNew() end
function testGone() end
testGone=nil`, []string{"TestFoo.testNew"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := Parse([]byte(tt.source))
			defer f.Close()
			tests := f.LuaUnitTests()
			var got []string
			if tests != nil {
				got = []string{}
			}
			for _, test := range tests {
				got = append(got, test.Name)
				if test.Start < 0 || test.End <= test.Start || test.End > len(tt.source) || !strings.Contains(tt.source[test.Start:test.End], "function") {
					t.Errorf("invalid test range: %+v", test)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}
