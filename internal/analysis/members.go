package analysis

import (
	"sort"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// Member is a statically named field in a table or a function declaration.
// Definition is the implementation a function value points at (through aliases);
// Declaration is where the field itself is first written or declared.
type Member struct {
	Name        string
	Function    bool
	Definition  *Definition
	Declaration *Definition
	SelfIndex   bool // the field is the exported table itself (`M.__index = M`)
}

// Definition is a source location independent of the lifetime of a parsed tree.
// URI is empty for the current file and filled by the module resolver.
type Definition struct {
	URI        string
	Start, End Position
	Parameters []string
	Method     bool
}

type memberValue struct {
	function     bool
	definition   *Definition
	fields       map[string]*memberValue
	decls        map[string]*Definition // first declaration site of each field
	origin       string                 // module file URI when the table is a resolved module export
	returns      *tree_sitter.Node
	index        *memberValue
	renameUnsafe bool // reference analysis cannot determine this table's bindings
}

func newMemberValue() *memberValue {
	return &memberValue{fields: map[string]*memberValue{}, decls: map[string]*Definition{}}
}

type memberWrite struct {
	root             *Symbol
	path             []string
	keyStart, keyEnd int // span of the last key, without string quotes
	at               int
	scope            *Scope
	value            *tree_sitter.Node
}

// collectMembers records writes without executing code or guessing dynamic keys.
// Queries replay preceding writes in enclosing scopes. This is intentionally
// conservative: no control-flow merging or execution of dynamic expressions.
func (f *File) collectMembers(n *tree_sitter.Node) {
	switch n.Kind() {
	case "assignment_statement":
		var targets, values []tree_sitter.Node
		cursor := n.Walk()
		for _, child := range n.NamedChildren(cursor) {
			switch child.Kind() {
			case "variable_list":
				targets = child.ChildrenByFieldName("name", cursor)
			case "expression_list":
				values = child.ChildrenByFieldName("value", cursor)
			}
		}
		cursor.Close()
		for i := range targets {
			var value *tree_sitter.Node
			if i < len(values) {
				value = &values[i]
			}
			f.recordMemberWrite(&targets[i], value, int(n.EndByte()), false)
		}
	case "function_declaration":
		if name := n.ChildByFieldName("name"); name != nil {
			f.recordMemberWrite(name, n, int(n.EndByte()), true)
		}
	}
	cursor := n.Walk()
	children := n.NamedChildren(cursor)
	cursor.Close()
	for i := range children {
		f.collectMembers(&children[i])
	}
}

func (f *File) recordMemberWrite(target, value *tree_sitter.Node, at int, function bool) {
	root, path := f.memberPath(target)
	if root == nil {
		return
	}
	sym, _ := f.SymbolAt(int(root.StartByte()))
	if sym == nil {
		return
	}
	scope := f.ScopeAt(int(target.StartByte()))
	if function && scope.Kind == ScopeFunction {
		scope = scope.Parent
	}
	write := memberWrite{root: sym, path: path, at: at, scope: scope, value: value}
	if len(path) > 0 {
		write.keyStart, write.keyEnd = f.keySpan(lastKeyNode(target))
	}
	f.memberWrites = append(f.memberWrites, write)
}

// lastKeyNode is the field, method or bracket key of an index expression.
func lastKeyNode(n *tree_sitter.Node) *tree_sitter.Node {
	switch n.Kind() {
	case "dot_index_expression", "bracket_index_expression":
		return n.ChildByFieldName("field")
	case "method_index_expression":
		return n.ChildByFieldName("method")
	}
	return nil
}

// keySpan is the byte range of a key's name: the identifier itself, or the
// contents of a plain quoted string, so edits can replace just the name.
func (f *File) keySpan(n *tree_sitter.Node) (int, int) {
	if n == nil {
		return 0, 0
	}
	start, end := int(n.StartByte()), int(n.EndByte())
	if n.Kind() == "string" && end-start >= 2 {
		return start + 1, end - 1
	}
	return start, end
}

func (f *File) memberPath(n *tree_sitter.Node) (*tree_sitter.Node, []string) {
	if n == nil {
		return nil, nil
	}
	if n.Kind() == "identifier" {
		return n, []string{}
	}
	field := "field"
	switch n.Kind() {
	case "dot_index_expression", "bracket_index_expression":
	case "method_index_expression":
		field = "method"
	default:
		return nil, nil
	}
	key := n.ChildByFieldName(field)
	name := f.staticKey(key, n.Kind() != "bracket_index_expression")
	if name == "" {
		return nil, nil
	}
	root, path := f.memberPath(n.ChildByFieldName("table"))
	return root, append(path, name)
}

func (f *File) staticKey(n *tree_sitter.Node, identifier bool) string {
	if n == nil {
		return ""
	}
	text := n.Utf8Text(f.Text)
	if identifier && n.Kind() == "identifier" {
		return text
	}
	// Only plain quoted identifier keys can be inserted after a dot. Escaped,
	// computed, numeric, and non-identifier keys require bracket expressions.
	if n.Kind() != "string" || len(text) < 2 {
		return ""
	}
	if text[0] != '\'' && text[0] != '"' {
		return ""
	}
	text = text[1 : len(text)-1]
	if !IsIdentifier(text) {
		return ""
	}
	return text
}

// IsIdentifier reports whether text is a Lua ASCII identifier, excluding keywords.
func IsIdentifier(text string) bool {
	if text == "" {
		return false
	}
	for i, c := range text {
		letter := c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && !(i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	keywords := " and break do else elseif end false for function goto if in local nil not or repeat return then true until while "
	return !strings.Contains(keywords, " "+text+" ")
}

// inferMemberValue follows values only through statically resolvable expressions.
// Shared pointers preserve table aliases; rebinding a name replaces its pointer.
func (f *File) inferMemberValue(n *tree_sitter.Node, values map[*Symbol]*memberValue, depth int) *memberValue {
	value := newMemberValue()
	if n == nil || depth > 16 {
		return value
	}
	if root, path := f.memberPath(n); root != nil {
		sym, _ := f.SymbolAt(int(root.StartByte()))
		current := values[sym]
		for _, name := range path {
			current = current.field(name, map[*memberValue]bool{})
		}
		if current != nil {
			return current
		}
		return value
	}
	switch n.Kind() {
	case "parenthesized_expression":
		return f.inferMemberValue(n.NamedChild(0), values, depth+1)
	case "function_definition", "function_declaration":
		value.function = true
		target := n.ChildByFieldName("name")
		if target == nil {
			target = n
		}
		if field := target.ChildByFieldName("field"); field != nil {
			target = field
		}
		if method := target.ChildByFieldName("method"); method != nil {
			target = method
		}
		value.definition = &Definition{Start: f.PositionOf(int(target.StartByte())), End: f.PositionOf(int(target.EndByte()))}
		parameters := (&walker{file: f}).parameterNames(n)
		value.definition.Parameters = []string{}
		if parameters != "" {
			value.definition.Parameters = strings.Split(parameters, ", ")
		}
		name := n.ChildByFieldName("name")
		value.definition.Method = name != nil && name.Kind() == "method_index_expression"
		// A single direct return is deterministic; conditional and multiple returns
		// require control-flow analysis and intentionally remain unknown.
		body := n.ChildByFieldName("body")
		if body != nil {
			for i := uint(0); i < body.NamedChildCount(); i++ {
				child := body.NamedChild(i)
				if child.Kind() == "return_statement" {
					list := child.NamedChild(0)
					if list != nil && list.NamedChildCount() == 1 {
						value.returns = list.NamedChild(0)
					}
				} else {
					value.returns = nil
					return value
				}
			}
		}
	case "function_call":
		fn := n.ChildByFieldName("name")
		args := n.ChildByFieldName("arguments")
		if fn == nil || args == nil {
			return value
		}
		if fn.Kind() == "identifier" && fn.Utf8Text(f.Text) == "require" && f.ModuleMembers != nil && args.NamedChildCount() == 1 {
			sym, _ := f.SymbolAt(int(fn.StartByte()))
			arg := args.NamedChild(0)
			if sym == nil && arg.Kind() == "string" {
				text := arg.Utf8Text(f.Text)
				if len(text) >= 2 && (text[0] == '\'' || text[0] == '"') {
					for _, member := range f.ModuleMembers(text[1 : len(text)-1]) {
						field := newMemberValue()
						field.function, field.definition = member.Function, member.Definition
						value.fields[member.Name] = field
						if member.SelfIndex {
							value.fields[member.Name] = value
						}
						if member.Declaration != nil {
							value.decls[member.Name] = member.Declaration
							value.origin = member.Declaration.URI
						}
					}
				}
			}
			return value
		}
		if fn.Kind() == "identifier" && fn.Utf8Text(f.Text) == "setmetatable" {
			sym, _ := f.SymbolAt(int(fn.StartByte()))
			if sym != nil || args.NamedChildCount() != 2 {
				return value
			}
			value = f.inferMemberValue(args.NamedChild(0), values, depth+1)
			meta := f.inferMemberValue(args.NamedChild(1), values, depth+1)
			value.index = meta.fields["__index"]
			return value
		}
		callee := f.inferMemberValue(fn, values, depth+1)
		if callee.returns != nil {
			// Restrict return inference to table literals, avoiding parameter/closure guesses.
			if callee.returns.Kind() == "table_constructor" {
				return f.inferMemberValue(callee.returns, values, depth+1)
			}
		}
	case "table_constructor":
		// Reference identities compare table pointers, so one constructor must
		// map to one value while identities are being computed.
		if cached := f.constructorValues[n.Id()]; cached != nil {
			return cached
		}
		if f.constructorValues != nil {
			f.constructorValues[n.Id()] = value
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			child := n.NamedChild(i)
			if child.Kind() != "field" {
				continue
			}
			first := child.Child(0)
			key := child.ChildByFieldName("name")
			name := f.staticKey(key, first != nil && first.Kind() != "[")
			if name != "" {
				value.fields[name] = f.inferMemberValue(child.ChildByFieldName("value"), values, depth+1)
				start, end := f.keySpan(key)
				value.decls[name] = &Definition{Start: f.PositionOf(start), End: f.PositionOf(end)}
			}
		}
	}
	return value
}

func (v *memberValue) field(name string, seen map[*memberValue]bool) *memberValue {
	if v == nil || seen[v] {
		return nil
	}
	seen[v] = true
	if field := v.fields[name]; field != nil {
		return field
	}
	return v.index.field(name, seen)
}

func (v *memberValue) members(out map[string]*memberValue, seen map[*memberValue]bool) {
	if v == nil || seen[v] {
		return
	}
	seen[v] = true
	v.index.members(out, seen)
	for name, field := range v.fields {
		out[name] = field
	}
}

// owner is the table in the __index chain that declares the field.
func (v *memberValue) owner(name string, seen map[*memberValue]bool) *memberValue {
	if v == nil || seen[v] {
		return nil
	}
	seen[v] = true
	if v.decls[name] != nil {
		return v
	}
	return v.index.owner(name, seen)
}

// KnownMembers returns fields for a simple receiver path at the cursor. The
// boolean says whether its root is declared here (including a non-table), so
// callers do not suggest standard-library fields for a shadowed library name.
func (f *File) KnownMembers(receiver []string, offset int) ([]Member, bool) {
	out := []Member{}
	if len(receiver) == 0 {
		return out, false
	}
	var root *Symbol
	for _, sym := range f.VisibleSymbols(offset) {
		if sym.Name == receiver[0] {
			root = sym
			break
		}
	}
	if root == nil {
		return out, false
	}
	values := f.memberState(offset)
	shape := values[root]
	if shape == nil {
		return out, true
	}
	for _, name := range receiver[1:] {
		shape = shape.field(name, map[*memberValue]bool{})
		if shape == nil {
			return out, true
		}
	}
	fields := map[string]*memberValue{}
	shape.members(fields, map[*memberValue]bool{})
	for name, value := range fields {
		out = append(out, Member{Name: name, Function: value.function})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, true
}

func (f *File) memberState(offset int) map[*Symbol]*memberValue {
	scopes := map[*Scope]bool{}
	for scope := f.ScopeAt(offset); scope != nil; scope = scope.Parent {
		scopes[scope] = true
	}
	return f.replayMembers(func(write memberWrite) bool { return write.at <= offset && scopes[write.scope] })
}

// writeKey identifies one member write across replays.
type writeKey struct {
	at   int
	root *Symbol
	path string
}

// inferWriteValue infers a write's value. While reference identities are being
// computed, results are cached so every replay binds the same table pointers:
// a write sees the same preceding writes in every replay that includes it.
func (f *File) inferWriteValue(write memberWrite, values map[*Symbol]*memberValue) *memberValue {
	if f.writeValues == nil {
		return f.inferMemberValue(write.value, values, 0)
	}
	key := writeKey{at: write.at, root: write.root, path: strings.Join(write.path, ".")}
	if value, ok := f.writeValues[key]; ok {
		return value
	}
	value := f.inferMemberValue(write.value, values, 0)
	f.writeValues[key] = value
	return value
}

// placeholderTable stands for a name whose table is never bound in this file
// (a parameter, an upvalue, an undeclared global). Shared while reference
// identities are being computed, for the same reason as inferWriteValue.
func (f *File) placeholderTable(root *Symbol) *memberValue {
	if f.writeValues == nil {
		return newMemberValue()
	}
	if value := f.rootPlaceholders[root]; value != nil {
		return value
	}
	value := newMemberValue()
	f.rootPlaceholders[root] = value
	return value
}

// replayMembers applies the selected writes in source order.
func (f *File) replayMembers(include func(memberWrite) bool) map[*Symbol]*memberValue {
	return f.replayMemberWrites(include, nil)
}

// observe sees the old and new values before each write, while they still
// retain their identity. Reference analysis uses it to detect unsafe aliases.
func (f *File) replayMemberWrites(include func(memberWrite) bool, observe func(memberWrite, *memberValue, *memberValue)) map[*Symbol]*memberValue {
	values := map[*Symbol]*memberValue{}
	writes := append([]memberWrite(nil), f.memberWrites...)
	sort.SliceStable(writes, func(i, j int) bool { return writes[i].at < writes[j].at })
	pending := map[int]*memberValue{}
	targets := map[int]*memberValue{}
	for i, write := range writes {
		if !include(write) {
			continue
		}
		value, ready := pending[i]
		if !ready {
			for j := i; j < len(writes) && writes[j].at == write.at; j++ {
				pending[j] = f.inferWriteValue(writes[j], values)
				if len(writes[j].path) > 0 {
					parent := values[writes[j].root]
					for _, name := range writes[j].path[:len(writes[j].path)-1] {
						if parent == nil {
							break
						}
						parent = parent.field(name, map[*memberValue]bool{})
					}
					targets[j] = parent
				}
			}
			value = pending[i]
		}
		if len(write.path) == 0 {
			if observe != nil {
				observe(write, values[write.root], value)
			}
			values[write.root] = value
			continue
		}
		parent := targets[i]
		if parent == nil {
			parent = values[write.root]
			if parent == nil {
				parent = f.placeholderTable(write.root)
				values[write.root] = parent
			}

			for _, name := range write.path[:len(write.path)-1] {
				if parent.fields[name] == nil {
					parent.fields[name] = newMemberValue()
				}
				parent = parent.fields[name]
			}
		}
		key := write.path[len(write.path)-1]
		if observe != nil {
			observe(write, parent.fields[key], value)
		}
		if parent.renameUnsafe {
			value.markRenameUnsafe()
		}
		// Declarations survive `t.x = nil`: the field is still the same member.
		if write.value != nil && write.value.Kind() == "nil" {
			delete(parent.fields, key)
		} else {
			parent.fields[key] = value
			if parent.decls[key] == nil {
				parent.decls[key] = &Definition{Start: f.PositionOf(write.keyStart), End: f.PositionOf(write.keyEnd)}
			}
		}
	}
	return values
}

// ExportedMembers returns the top-level fields of a module's direct return.
// Conditional returns and runtime-dependent loading remain unknown.
func (f *File) ExportedMembers() []Member {
	return f.exportedMembers(false)
}

// ExportedDefinitions includes implementation locations for exported functions.
func (f *File) ExportedDefinitions() []Member {
	return f.exportedMembers(true)
}

func (f *File) exportedMembers(definitions bool) []Member {
	root := f.tree.RootNode()
	for i := uint(0); i < root.NamedChildCount(); i++ {
		node := root.NamedChild(i)
		if node.Kind() != "return_statement" {
			continue
		}
		list := node.NamedChild(0)
		if list == nil || list.NamedChildCount() != 1 {
			return nil
		}
		shape := f.inferMemberValue(list.NamedChild(0), f.memberState(int(node.StartByte())), 0)
		fields := map[string]*memberValue{}
		shape.members(fields, map[*memberValue]bool{})
		members := []Member{}
		for name, value := range fields {
			member := Member{Name: name, Function: value.function}
			if definitions {
				member.Definition = value.definition
				member.SelfIndex = value == shape
				if owner := shape.owner(name, map[*memberValue]bool{}); owner != nil {
					member.Declaration = owner.decls[name]
				}
			}
			members = append(members, member)
		}
		sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
		return members
	}
	return nil
}

// InStringOrComment suppresses code completions inside literals and comments.
func (f *File) InStringOrComment(offset int) bool {
	if offset <= 0 || offset > len(f.Text) {
		return false
	}
	n := f.tree.RootNode().NamedDescendantForByteRange(uint(offset-1), uint(offset-1))
	for ; n != nil; n = n.Parent() {
		if n.Kind() == "string" || n.Kind() == "comment" {
			return true
		}
	}
	return false
}

// ImplementationAt follows a function value through static member writes and aliases.
func (f *File) ImplementationAt(offset int) *Definition {
	identifierByte := func(c byte) bool {
		return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	}
	if offset > 0 && offset <= len(f.Text) && identifierByte(f.Text[offset-1]) &&
		(offset == len(f.Text) || !identifierByte(f.Text[offset])) {
		offset--
	}
	if offset < 0 || offset >= len(f.Text) {
		return nil
	}
	n := f.tree.RootNode().NamedDescendantForByteRange(uint(offset), uint(offset))
	if n == nil {
		return nil
	}
	if parent := n.Parent(); parent != nil {
		for _, field := range []string{"field", "method"} {
			key := parent.ChildByFieldName(field)
			if key != nil && int(key.StartByte()) <= offset && offset < int(key.EndByte()) {
				n = parent
				break
			}
		}
	}
	switch n.Kind() {
	case "identifier", "dot_index_expression", "method_index_expression", "bracket_index_expression":
	default:
		return nil
	}
	return f.inferMemberValue(n, f.memberState(offset), 0).definition
}
