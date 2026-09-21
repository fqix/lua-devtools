package analysis

import (
	"strings"
	"testing"
)

// spansText renders spans as the covered source text, in order.
func sites(f *File, identity MemberIdentity, self string) []Span {
	spans, _ := f.MemberSites(identity, self)
	return spans
}

func spansText(f *File, spans []Span) []string {
	out := []string{}
	for _, span := range spans {
		out = append(out, string(f.Text[span.Start:span.End]))
	}
	return out
}

func lineOf(f *File, span Span) int {
	return f.PositionOf(span.Start).Line + 1
}

func linesOf(f *File, spans []Span) []int {
	out := []int{}
	for _, span := range spans {
		out = append(out, lineOf(f, span))
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMemberReferencesFollowTables(t *testing.T) {
	t.Parallel()
	source := strings.Join([]string{
		"local M = {}",               // 1
		"function M.foo() end",       // 2
		"M.foo()",                    // 3
		"local alias = M",            // 4
		"alias.foo()",                // 5
		"local other = {foo = 1}",    // 6
		"print(other.foo, M['foo'])", // 7
		"M.foo = nil",                // 8
	}, "\n")
	f := Parse([]byte(source))
	defer f.Close()
	target := f.ReferenceTargetAt(strings.Index(source, "alias.foo")+6, "file:///a.lua")
	if target == nil || target.Member == nil || target.Member.Name != "foo" || target.Member.Owner != "" {
		t.Fatalf("target = %+v", target)
	}
	if got := linesOf(f, sites(f, *target.Member, "file:///a.lua")); !equalInts(got, []int{2, 3, 5, 7, 8}) {
		t.Fatalf("member sites on lines %v", got)
	}
	// The unrelated table's field is a different member.
	other := f.ReferenceTargetAt(strings.Index(source, "other.foo")+6, "file:///a.lua")
	if got := linesOf(f, sites(f, *other.Member, "file:///a.lua")); !equalInts(got, []int{6, 7}) {
		t.Fatalf("other sites on lines %v", got)
	}
	if !f.MemberDeclares(*other.Member, "foo", "file:///a.lua") || f.MemberDeclares(*other.Member, "bar", "file:///a.lua") {
		t.Fatal("MemberDeclares should see only declared fields")
	}
}

func TestMemberReferencesThroughMetatables(t *testing.T) {
	t.Parallel()
	source := strings.Join([]string{
		"local C = {}",         // 1
		"C.__index = C",        // 2
		"function C:run() end", // 3
		"function C.new() return setmetatable({}, C) end", // 4
		"local o = setmetatable({}, C)",                   // 5
		"o:run()",                                         // 6
		"local t = { ['run'] = 1 }",                       // 7
		"t.run()",                                         // 8
	}, "\n")
	f := Parse([]byte(source))
	defer f.Close()
	target := f.ReferenceTargetAt(strings.Index(source, "o:run")+3, "")
	if target == nil || target.Member == nil {
		t.Fatalf("target = %+v", target)
	}
	if got := linesOf(f, sites(f, *target.Member, "")); !equalInts(got, []int{3, 6}) {
		t.Fatalf("method sites on lines %v", got)
	}
	quoted := f.ReferenceTargetAt(strings.Index(source, "['run']")+3, "")
	if got := spansText(f, sites(f, *quoted.Member, "")); strings.Join(got, ",") != "run,run" {
		t.Fatalf("quoted key spans = %q", got)
	}
	if !f.MemberDeclares(*target.Member, "new", "") {
		t.Fatal("instances inherit declarations through __index")
	}
}

func TestMemberReferencesAcrossModules(t *testing.T) {
	t.Parallel()
	moduleURI := "file:///mod.lua"
	moduleSource := "local M = {}\nM.__index = M\nfunction M.foo() end\nM.count = 0\nfunction M:method() end\nreturn M\n"
	consumerSource := strings.Join([]string{
		"local m = require('mod')",        // 1
		"m.foo()",                         // 2
		"print(m.count)",                  // 3
		"local obj = setmetatable({}, m)", // 4
		"obj:method()",                    // 5
		"local unrelated = { foo = 2 }",   // 6
		"unrelated.foo()",                 // 7
	}, "\n")
	resolver := func(name string) []Member {
		if name != "mod" {
			return nil
		}
		module := Parse([]byte(moduleSource))
		defer module.Close()
		members := module.ExportedDefinitions()
		for i := range members {
			if members[i].Declaration != nil {
				copy := *members[i].Declaration
				copy.URI = moduleURI
				members[i].Declaration = &copy
			}
		}
		return members
	}
	consumer := Parse([]byte(consumerSource))
	defer consumer.Close()
	consumer.ModuleMembers = resolver
	target := consumer.ReferenceTargetAt(strings.Index(consumerSource, "m.foo")+2, "file:///main.lua")
	if target == nil || target.Member == nil || target.Member.Owner != moduleURI || target.Member.Name != "foo" {
		t.Fatalf("consumer target = %+v", target)
	}
	if got := linesOf(consumer, sites(consumer, *target.Member, "file:///main.lua")); !equalInts(got, []int{2}) {
		t.Fatalf("consumer sites on lines %v", got)
	}
	module := Parse([]byte(moduleSource))
	defer module.Close()
	if got := linesOf(module, sites(module, *target.Member, moduleURI)); !equalInts(got, []int{3}) {
		t.Fatalf("module sites on lines %v", got)
	}
	// The module's own view of the member has the same identity.
	own := module.ReferenceTargetAt(strings.Index(moduleSource, "M.foo")+2, moduleURI)
	if own == nil || own.Member == nil || own.Member.Owner != moduleURI {
		t.Fatalf("module target = %+v", own)
	}
	if !module.MemberDeclares(*target.Member, "count", moduleURI) || module.MemberDeclares(*target.Member, "missing", moduleURI) {
		t.Fatal("module declarations")
	}
	// Methods reached through a required class's __index resolve too.
	method := consumer.ReferenceTargetAt(strings.Index(consumerSource, "obj:method")+4, "file:///main.lua")
	if method == nil || method.Member == nil || method.Member.Owner != moduleURI {
		t.Fatalf("method target = %+v", method)
	}
	if got := linesOf(module, sites(module, *method.Member, moduleURI)); !equalInts(got, []int{5}) {
		t.Fatalf("method sites in module on lines %v", got)
	}
	// Data fields declare where they are first written.
	decl := consumer.MemberDeclarationAt(strings.Index(consumerSource, "m.count") + 2)
	if decl == nil || decl.URI != moduleURI || decl.Start.Line != 3 {
		t.Fatalf("count declaration = %+v", decl)
	}
}

func TestGlobalReferencesAndRenameConflicts(t *testing.T) {
	t.Parallel()
	source := strings.Join([]string{
		"function greet() end", // 1
		"greet()",              // 2
		"local function f() local greet = 1 return greet end", // 3
		"greet()",   // 4
		"other = 1", // 5
	}, "\n")
	f := Parse([]byte(source))
	defer f.Close()
	target := f.ReferenceTargetAt(strings.Index(source, "greet()"), "")
	if target == nil || target.Global != "greet" {
		t.Fatalf("target = %+v", target)
	}
	if got := linesOf(f, f.GlobalSites("greet", true)); !equalInts(got, []int{1, 2, 4}) {
		t.Fatalf("global sites on lines %v", got)
	}
	if err := f.GlobalRenameConflict("greet", "f"); err == nil {
		t.Fatal("renaming to a captured local must fail")
	}
	if err := f.GlobalRenameConflict("greet", "other"); err == nil {
		t.Fatal("renaming onto another global must fail")
	}
	if err := f.GlobalRenameConflict("greet", "hello"); err != nil {
		t.Fatal(err)
	}
	local := f.ReferenceTargetAt(strings.Index(source, "return greet")+7, "")
	if local == nil || local.Symbol == nil || local.Symbol.Global {
		t.Fatalf("local target = %+v", local)
	}
}

func TestScopedQueriesUnaffectedByReferenceState(t *testing.T) {
	t.Parallel()
	source := "local m = {}\nm.|\nm.later = 1"
	cursor := strings.Index(source, "|")
	text := strings.Replace(source, "|", "", 1)
	f := Parse([]byte(text))
	defer f.Close()
	sites(f, MemberIdentity{Name: "later"}, "")
	f.ResetReferences()
	members, _ := f.KnownMembers([]string{"m"}, cursor)
	if len(members) != 0 {
		t.Fatalf("future field leaked into completion: %+v", members)
	}
}

func TestMemberReferencesFollowRebinding(t *testing.T) {
	t.Parallel()
	source := strings.Join([]string{
		"local t = {foo = 1}", // 1
		"print(t.foo)",        // 2
		"t = {foo = 2}",       // 3
		"print(t.foo)",        // 4
	}, "\n")
	f := Parse([]byte(source))
	defer f.Close()
	first := f.ReferenceTargetAt(strings.Index(source, "print(t.foo)")+8, "")
	if first == nil || first.Member == nil {
		t.Fatalf("target = %+v", first)
	}
	spans, ambiguous := f.MemberSites(*first.Member, "")
	if got := linesOf(f, spans); !equalInts(got, []int{1, 2}) || ambiguous {
		t.Fatalf("first table sites %v ambiguous=%v", got, ambiguous)
	}
	second := f.ReferenceTargetAt(strings.LastIndex(source, "t.foo")+2, "")
	spans, ambiguous = f.MemberSites(*second.Member, "")
	if got := linesOf(f, spans); !equalInts(got, []int{3, 4}) || ambiguous {
		t.Fatalf("second table sites %v ambiguous=%v", got, ambiguous)
	}
}

func TestMemberReferencesUndecidableBindings(t *testing.T) {
	t.Parallel()
	for name, source := range map[string]string{
		"branch":   "local t = {foo = 1}\nif x then t = {foo = 2} end\nprint(t.foo)",
		"loop":     "local t = {foo = 1}\nwhile x do print(t.foo); t = {foo = 2} end",
		"function": "local t = {foo = 1}\nlocal function reset() t = {foo = 2} end\nprint(t.foo)",
		"closure":  "local t = {foo = 1}\nlocal function read() return t.foo end\nt = {foo = 2}",
	} {
		t.Run(name, func(t *testing.T) {
			f := Parse([]byte(source))
			defer f.Close()
			target := f.ReferenceTargetAt(strings.Index(source, "{foo = 1}")+1, "")
			if target == nil || target.Member == nil {
				t.Fatalf("target = %+v", target)
			}
			if _, ambiguous := f.MemberSites(*target.Member, ""); !ambiguous {
				t.Fatal("rebinding must be reported as undecidable")
			}
		})
	}
	// Straight-line reassignment after the site is decidable.
	source := "local t = {foo = 1}\ndo print(t.foo) end\nt = {foo = 2}\nprint(t.foo)"
	f := Parse([]byte(source))
	defer f.Close()
	target := f.ReferenceTargetAt(strings.Index(source, "{foo = 1}")+1, "")
	if spans, ambiguous := f.MemberSites(*target.Member, ""); ambiguous || !equalInts(linesOf(f, spans), []int{1, 2}) {
		t.Fatalf("straight-line sites %v ambiguous=%v", linesOf(f, spans), ambiguous)
	}
}

func TestMemberReferencesOnUnboundTables(t *testing.T) {
	t.Parallel()
	source := "local function f(t)\n  t.x = 1\n  return t.x\nend"
	f := Parse([]byte(source))
	defer f.Close()
	target := f.ReferenceTargetAt(strings.Index(source, "t.x = 1")+2, "")
	if target == nil || target.Member == nil {
		t.Fatalf("target = %+v", target)
	}
	if spans, ambiguous := f.MemberSites(*target.Member, ""); ambiguous || !equalInts(linesOf(f, spans), []int{2, 3}) {
		t.Fatalf("parameter table sites %v ambiguous=%v", linesOf(f, spans), ambiguous)
	}
}

func TestMemberRenameSafetyDoesNotSpreadToUnrelatedTables(t *testing.T) {
	source := "local t={child={foo=1},safe=1}\nlocal other={foo=1}\nt.child={foo=2}\nprint(t.safe,other.foo)\n"
	f := Parse([]byte(source))
	defer f.Close()
	for _, needle := range []string{"safe=1", "other.foo"} {
		offset := strings.Index(source, needle)
		if needle == "other.foo" {
			offset += len("other.")
		}
		target := f.ReferenceTargetAt(offset, "")
		if target == nil || target.Member == nil {
			t.Fatalf("target %q: %+v", needle, target)
		}
		spans, ambiguous := f.MemberSites(*target.Member, "")
		if ambiguous || len(spans) != 2 {
			t.Fatalf("%s: %v, ambiguous=%v", needle, spans, ambiguous)
		}
	}
}
