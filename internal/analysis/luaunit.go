package analysis

import (
	"sort"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// TestCase is a statically declared test selectable by its framework CLI.
type TestCase struct {
	Name       string
	Start, End int
}

// LuaUnitTests discovers top-level global test functions and test-table methods
// in files importing luaunit. Local, nested and dynamically registered tests are
// deliberately excluded: LuaUnit's default CLI lookup cannot resolve them.
func (f *File) LuaUnitTests() []TestCase {
	if !f.importsLuaUnit(f.tree.RootNode()) {
		return nil
	}
	tests := map[string]TestCase{}
	for _, write := range f.memberWrites {
		if write.scope != f.Root || !write.root.Global || !luaUnitName(write.root.Name) {
			continue
		}
		root := write.root.Name
		if len(write.path) == 0 {
			// Rebinding a suite replaces its earlier methods.
			for name := range tests {
				if name == root || strings.HasPrefix(name, root+".") {
					delete(tests, name)
				}
			}
			if isLuaFunction(write.value) {
				tests[root] = testCase(root, write.value)
			} else if write.value != nil && write.value.Kind() == "table_constructor" {
				for i := uint(0); i < write.value.NamedChildCount(); i++ {
					field := write.value.NamedChild(i)
					if field.Kind() != "field" {
						continue
					}
					first := field.Child(0)
					name := f.staticKey(field.ChildByFieldName("name"), first != nil && first.Kind() != "[")
					if luaUnitName(name) && isLuaFunction(field.ChildByFieldName("value")) {
						tests[root+"."+name] = testCase(root+"."+name, field)
					}
				}
			}
		} else if len(write.path) == 1 && luaUnitName(write.path[0]) {
			name := root + "." + write.path[0]
			delete(tests, name)
			if isLuaFunction(write.value) {
				tests[name] = testCase(name, write.value)
			}
		}
	}
	out := make([]TestCase, 0, len(tests))
	for _, test := range tests {
		out = append(out, test)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

func luaUnitName(name string) bool {
	return strings.HasPrefix(name, "test") || strings.HasPrefix(name, "Test")
}

func isLuaFunction(node *tree_sitter.Node) bool {
	return node != nil && (node.Kind() == "function_definition" || node.Kind() == "function_declaration")
}

func testCase(name string, node *tree_sitter.Node) TestCase {
	return TestCase{Name: name, Start: int(node.StartByte()), End: int(node.EndByte())}
}

func (f *File) importsLuaUnit(node *tree_sitter.Node) bool {
	if isLuaFunction(node) {
		return false
	}
	if node.Kind() == "function_call" && f.ScopeAt(int(node.StartByte())) == f.Root {
		name, args := node.ChildByFieldName("name"), node.ChildByFieldName("arguments")
		if name != nil && name.Kind() == "identifier" && name.Utf8Text(f.Text) == "require" && args != nil && args.NamedChildCount() == 1 {
			sym, _ := f.SymbolAt(int(name.StartByte()))
			arg := args.NamedChild(0)
			if sym == nil && arg.Kind() == "string" {
				text := arg.Utf8Text(f.Text)
				if text == `"luaunit"` || text == `'luaunit'` || text == `[[luaunit]]` {
					return true
				}
			}
		}
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		if f.importsLuaUnit(node.NamedChild(i)) {
			return true
		}
	}
	return false
}
