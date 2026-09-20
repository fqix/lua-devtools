// Package analysis parses Lua source with tree-sitter and builds the scope and
// symbol information the language server needs: syntax diagnostics, a scope tree
// with local/parameter/function/global symbols, and resolved identifier references.
//
// Positions inside this package are byte offsets into the source. Conversion to
// LSP line/character (UTF-16) positions lives in position.go.
package analysis

import (
	"fmt"
	"sort"
	"strings"

	tree_sitter_lua "github.com/tree-sitter-grammars/tree-sitter-lua/bindings/go"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

var luaLanguage = tree_sitter.NewLanguage(tree_sitter_lua.Language())

type SymbolKind int

const (
	KindLocal SymbolKind = iota
	KindParameter
	KindFunction // local or global function declared with `function name(...)`
	KindGlobal   // explicit global declaration or first assignment to an undeclared name
	KindField    // `function M.f()` / `function M:f()`; only for document symbols
)

func (k SymbolKind) String() string {
	switch k {
	case KindLocal:
		return "local"
	case KindParameter:
		return "parameter"
	case KindFunction:
		return "function"
	case KindGlobal:
		return "global"
	case KindField:
		return "field"
	}
	return "unknown"
}

// Symbol is one declaration.
type Symbol struct {
	Name string
	Kind SymbolKind
	// Start/End cover the identifier that declares the symbol (definition location).
	Start, End int
	// FullStart/FullEnd cover the whole declaration (a function's body) for outlines.
	FullStart, FullEnd int
	// VisibleFrom is the byte offset from which the name resolves to this symbol:
	// `local x = x` must not see itself in its own initializer.
	VisibleFrom int
	// Detail is a one-line signature such as "function add(a, b)".
	Detail string
	// Global marks names that live in _G (assigned globals and global functions).
	Global bool
	// ExplicitGlobal is a Lua 5.5 global declaration with lexical visibility.
	ExplicitGlobal bool
	// Scope is where the symbol is declared; Inner is a function's own scope.
	Scope *Scope
	Inner *Scope
}

type ScopeKind int

const (
	ScopeChunk ScopeKind = iota
	ScopeBlock
	ScopeFunction
)

// Scope is a lexical scope: the chunk, a block, or a function (parameters + body).
type Scope struct {
	Kind               ScopeKind
	Parent             *Scope
	Start, End         int
	Symbols            []*Symbol
	Children           []*Scope
	Owner              *Symbol // the function symbol that owns a ScopeFunction, if named
	globalDeclarations []globalDeclaration
}

type globalDeclaration struct {
	at       int
	wildcard bool
}

// allowsImplicitGlobals distinguishes the chunk's implicit global-by-default
// mode from an explicit global * declaration, which survives named declarations.
func (s *Scope) allowsImplicitGlobals(at int) bool {
	declared := false
	for current := s; current != nil; current = current.Parent {
		for _, declaration := range current.globalDeclarations {
			if declaration.at <= at {
				if declaration.wildcard {
					return true
				}
				declared = true
			}
		}
	}
	return !declared
}

// Reference is one use of a name, resolved to a symbol when possible.
type Reference struct {
	Name       string
	Start, End int
	Scope      *Scope
	Symbol     *Symbol // nil for an unknown global
}

// Diagnostic carries a message key (see internal/i18n) plus its argument so the
// language server can render it in the client's locale.
type Diagnostic struct {
	Start, End  int
	Key         string // "diagnostic.missing" | "diagnostic.unexpected"
	Arg         string
	Construct   string
	OpeningLine int
}

type File struct {
	// ModuleMembers optionally resolves statically named module exports without execution.
	ModuleMembers func(string) []Member
	Text          []byte
	Root          *Scope
	Diagnostics   []Diagnostic
	References    []Reference
	Globals       map[string]*Symbol
	lines         []int // byte offset of every line start

	tree         *tree_sitter.Tree
	memberWrites []memberWrite
}

// Parse analyses one document. Call Close when done with it.
func Parse(text []byte) *File {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(luaLanguage); err != nil {
		panic(err)
	}
	tree := parser.Parse(text, nil)

	f := &File{
		Text:    text,
		Globals: map[string]*Symbol{},
		tree:    tree,
	}
	f.indexLines()

	root := tree.RootNode()
	f.Root = &Scope{Kind: ScopeChunk, Start: 0, End: len(text)}
	f.collectDiagnostics(root)
	w := &walker{file: f}
	w.walk(root, f.Root)
	// Globals may be assigned after their first use; bind those late.
	for i := range f.References {
		if f.References[i].Symbol == nil && f.References[i].Scope.allowsImplicitGlobals(f.References[i].Start) {
			f.References[i].Symbol = f.Globals[f.References[i].Name]
		}
	}
	f.collectMembers(root)
	return f
}

func (f *File) Close() {
	if f.tree != nil {
		f.tree.Close()
		f.tree = nil
	}
}

// --- diagnostics -------------------------------------------------------------

const maxDiagnostics = 50

func (f *File) collectDiagnostics(root *tree_sitter.Node) {
	if !root.HasError() {
		return
	}
	if diagnostic := f.unclosedConstruct(root); diagnostic != nil {
		f.Diagnostics = append(f.Diagnostics, *diagnostic)
		return
	}
	var visit func(n *tree_sitter.Node)
	visit = func(n *tree_sitter.Node) {
		if len(f.Diagnostics) >= maxDiagnostics {
			return
		}
		switch {
		case n.IsMissing():
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Start: int(n.StartByte()),
				End:   int(n.EndByte()),
				Key:   "diagnostic.missing",
				Arg:   n.Kind(),
			})
			return
		case n.IsError():
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Start: int(n.StartByte()),
				End:   int(n.EndByte()),
				Key:   "diagnostic.unexpected",
				Arg:   describeError(n, f.Text),
			})
			return
		case !n.HasError():
			return
		}
		cursor := n.Walk()
		for _, child := range n.Children(cursor) {
			visit(&child)
		}
		cursor.Close()
	}
	visit(root)
}

// describeError returns a quoted excerpt of the error node, or "" at end of input.
func describeError(n *tree_sitter.Node, text []byte) string {
	s := strings.TrimSpace(n.Utf8Text(text))
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i] + "..."
	}
	if len(s) > 40 {
		s = s[:40] + "..."
	}
	return fmt.Sprintf("%q", s)
}

// --- scope walk ----------------------------------------------------------------

type walker struct {
	file *File
}

func (w *walker) newScope(kind ScopeKind, parent *Scope, n *tree_sitter.Node) *Scope {
	s := &Scope{Kind: kind, Parent: parent, Start: int(n.StartByte()), End: int(n.EndByte())}
	parent.Children = append(parent.Children, s)
	return s
}

func (w *walker) declare(scope *Scope, ident *tree_sitter.Node, kind SymbolKind, visibleFrom int) *Symbol {
	sym := &Symbol{
		Name:        ident.Utf8Text(w.file.Text),
		Kind:        kind,
		Start:       int(ident.StartByte()),
		End:         int(ident.EndByte()),
		FullStart:   int(ident.StartByte()),
		FullEnd:     int(ident.EndByte()),
		VisibleFrom: visibleFrom,
		Scope:       scope,
	}
	sym.Detail = kind.String() + " " + sym.Name
	scope.Symbols = append(scope.Symbols, sym)
	return sym
}

func (w *walker) walkChildren(n *tree_sitter.Node, scope *Scope) {
	cursor := n.Walk()
	children := n.Children(cursor)
	cursor.Close()
	for i := range children {
		w.walk(&children[i], scope)
	}
}

func (w *walker) walkField(n *tree_sitter.Node, field string, scope *Scope) {
	if c := n.ChildByFieldName(field); c != nil {
		w.walk(c, scope)
	}
}

func (w *walker) walk(n *tree_sitter.Node, scope *Scope) {
	if n == nil || n.IsMissing() {
		return
	}
	switch n.Kind() {
	case "comment", "string", "number":
		return

	case "block":
		w.walkChildren(n, w.newScope(ScopeBlock, scope, n))

	case "function_declaration":
		w.functionDeclaration(n, scope)

	case "function_definition":
		fn := w.newScope(ScopeFunction, scope, n)
		w.declareParameters(n, fn)
		w.walkField(n, "body", fn)

	case "implicit_variable_declaration":
		scope.globalDeclarations = append(scope.globalDeclarations, globalDeclaration{at: int(n.EndByte()), wildcard: true})

	case "variable_declaration":
		w.variableDeclaration(n, scope)

	case "assignment_statement":
		w.assignment(n, scope)

	case "repeat_statement":
		// Locals declared in the body stay visible in the `until` condition, so
		// one scope covers both instead of the body's own block scope.
		rs := w.newScope(ScopeBlock, scope, n)
		if body := n.ChildByFieldName("body"); body != nil {
			w.walkChildren(body, rs)
		}
		w.walkField(n, "condition", rs)

	case "for_statement":
		loop := w.newScope(ScopeBlock, scope, n)
		clause := n.ChildByFieldName("clause")
		if clause != nil {
			switch clause.Kind() {
			case "for_numeric_clause":
				w.walkField(clause, "start", loop)
				w.walkField(clause, "end", loop)
				w.walkField(clause, "step", loop)
				if name := clause.ChildByFieldName("name"); name != nil {
					w.declare(loop, name, KindLocal, int(clause.EndByte()))
				}
			case "for_generic_clause":
				cursor := clause.Walk()
				for _, c := range clause.NamedChildren(cursor) {
					if c.Kind() == "expression_list" {
						w.walk(&c, loop)
					}
				}
				for _, c := range clause.NamedChildren(cursor) {
					if c.Kind() == "variable_list" {
						w.declareVariableList(&c, loop, KindLocal, int(clause.EndByte()))
					}
				}
				cursor.Close()
			}
		}
		w.walkField(n, "body", loop)

	case "identifier":
		w.identifier(n, scope)

	default:
		w.walkChildren(n, scope)
	}
}

func (w *walker) declareParameters(fn *tree_sitter.Node, scope *Scope) {
	params := fn.ChildByFieldName("parameters")
	if params == nil {
		return
	}
	cursor := params.Walk()
	for _, p := range params.ChildrenByFieldName("name", cursor) {
		w.declare(scope, &p, KindParameter, int(p.StartByte()))
	}
	cursor.Close()
}

func (w *walker) parameterNames(fn *tree_sitter.Node) string {
	params := fn.ChildByFieldName("parameters")
	if params == nil {
		return ""
	}
	var names []string
	cursor := params.Walk()
	for _, p := range params.NamedChildren(cursor) {
		if p.Kind() == "comment" {
			continue
		}
		if p.Kind() == "identifier" && len(names) > 0 && names[len(names)-1] == "..." {
			names[len(names)-1] += p.Utf8Text(w.file.Text)
		} else {
			names = append(names, p.Utf8Text(w.file.Text))
		}
	}
	cursor.Close()
	return strings.Join(names, ", ")
}

func (w *walker) functionDeclaration(n *tree_sitter.Node, scope *Scope) {
	name := n.ChildByFieldName("name")
	var sym *Symbol
	// `local function f` is a function_declaration attached to its parent through
	// the `local_declaration` field; there is no wrapping variable_declaration node.
	isLocal := fieldNameOf(n) == "local_declaration"
	isGlobal := fieldNameOf(n) == "global_declaration"
	if isGlobal {
		scope.globalDeclarations = append(scope.globalDeclarations, globalDeclaration{at: int(n.StartByte())})
	}

	if name != nil {
		switch name.Kind() {
		case "identifier":
			if isGlobal {
				sym = w.declare(scope, name, KindFunction, int(n.StartByte()))
				sym.Global, sym.ExplicitGlobal = true, true
			} else if isLocal {
				// `local function f` is visible inside its own body (recursion).
				sym = w.declare(scope, name, KindFunction, int(n.StartByte()))
			} else if existing := w.resolve(scope, name.Utf8Text(w.file.Text), int(name.StartByte())); existing != nil {
				// `function f()` where f is an existing local: an assignment.
				w.file.References = append(w.file.References, Reference{
					Name: existing.Name, Start: int(name.StartByte()), End: int(name.EndByte()), Scope: scope, Symbol: existing,
				})
				sym = existing
			} else if scope.allowsImplicitGlobals(int(name.StartByte())) {
				sym = w.declareGlobal(name, KindFunction, scope)
			}
		case "dot_index_expression", "method_index_expression":
			// `function M.f()` / `function M:f()`: the table is a reference, the
			// whole thing is a field symbol for the outline only.
			w.walkField(name, "table", scope)
			sym = &Symbol{
				Name:        name.Utf8Text(w.file.Text),
				Kind:        KindField,
				Start:       int(name.StartByte()),
				End:         int(name.EndByte()),
				VisibleFrom: int(n.EndByte()),
				Scope:       scope,
			}
			scope.Symbols = append(scope.Symbols, sym)
		}
	}

	fn := w.newScope(ScopeFunction, scope, n)
	if sym != nil {
		if !sym.ExplicitGlobal || sym.FullStart >= int(n.StartByte()) {
			sym.FullStart = int(n.StartByte())
		}
		sym.FullEnd = int(n.EndByte())
		sym.Detail = fmt.Sprintf("function %s(%s)", sym.Name, w.parameterNames(n))
		if sym.ExplicitGlobal {
			sym.Detail = "global " + sym.Detail
		}
		if sym.Inner == nil {
			sym.Inner = fn
		}
		fn.Owner = sym
	}
	w.declareParameters(n, fn)
	w.walkField(n, "body", fn)
}

func (w *walker) variableDeclaration(n *tree_sitter.Node, scope *Scope) {
	kind := KindLocal
	if fieldNameOf(n) == "global_declaration" {
		kind = KindGlobal
		scope.globalDeclarations = append(scope.globalDeclarations, globalDeclaration{at: int(n.EndByte())})
	}
	cursor := n.Walk()
	children := n.NamedChildren(cursor)
	cursor.Close()
	for i := range children {
		c := &children[i]
		switch c.Kind() {
		case "function_declaration":
			w.functionDeclaration(c, scope)
		case "assignment_statement":
			// Initializers run before the new locals exist.
			cur := c.Walk()
			parts := c.NamedChildren(cur)
			cur.Close()
			for j := range parts {
				if parts[j].Kind() == "expression_list" {
					w.walk(&parts[j], scope)
				}
			}
			for j := range parts {
				if parts[j].Kind() == "variable_list" {
					w.declareVariableList(&parts[j], scope, kind, int(n.EndByte()))
				}
			}
		case "variable_list":
			// `local a, b` without initializer.
			w.declareVariableList(c, scope, kind, int(n.EndByte()))
		default:
			w.walk(c, scope)
		}
	}
}

func (w *walker) declareVariableList(list *tree_sitter.Node, scope *Scope, kind SymbolKind, visibleFrom int) {
	cursor := list.Walk()
	for _, name := range list.ChildrenByFieldName("name", cursor) {
		if name.Kind() == "identifier" {
			sym := w.declare(scope, &name, kind, visibleFrom)
			if kind == KindGlobal {
				sym.Global, sym.ExplicitGlobal = true, true
			}
		} else {
			w.walk(&name, scope)
		}
	}
	cursor.Close()
}

func (w *walker) assignment(n *tree_sitter.Node, scope *Scope) {
	cursor := n.Walk()
	parts := n.NamedChildren(cursor)
	cursor.Close()
	for i := range parts {
		if parts[i].Kind() == "expression_list" {
			w.walk(&parts[i], scope)
		}
	}
	for i := range parts {
		if parts[i].Kind() != "variable_list" {
			continue
		}
		cur := parts[i].Walk()
		for _, target := range parts[i].ChildrenByFieldName("name", cur) {
			if target.Kind() == "identifier" {
				w.identifierOrGlobalDefinition(&target, scope)
			} else {
				w.walk(&target, scope)
			}
		}
		cur.Close()
	}
}

// identifierOrGlobalDefinition handles an assignment target: a known local is a
// reference, an unknown name becomes a global (its first assignment is the definition).
func (w *walker) identifierOrGlobalDefinition(ident *tree_sitter.Node, scope *Scope) {
	name := ident.Utf8Text(w.file.Text)
	if sym := w.resolve(scope, name, int(ident.StartByte())); sym != nil {
		w.file.References = append(w.file.References, Reference{
			Name: name, Start: int(ident.StartByte()), End: int(ident.EndByte()), Scope: scope, Symbol: sym,
		})
		return
	}
	if !scope.allowsImplicitGlobals(int(ident.StartByte())) {
		w.identifier(ident, scope)
		return
	}
	if existing, ok := w.file.Globals[name]; ok {
		w.file.References = append(w.file.References, Reference{
			Name: name, Start: int(ident.StartByte()), End: int(ident.EndByte()), Scope: scope, Symbol: existing,
		})
		return
	}
	w.declareGlobal(ident, KindGlobal, scope)
}

func (w *walker) declareGlobal(ident *tree_sitter.Node, kind SymbolKind, scope *Scope) *Symbol {
	sym := &Symbol{
		Name:        ident.Utf8Text(w.file.Text),
		Kind:        kind,
		Start:       int(ident.StartByte()),
		End:         int(ident.EndByte()),
		FullStart:   int(ident.StartByte()),
		FullEnd:     int(ident.EndByte()),
		VisibleFrom: 0, // globals are visible everywhere (at runtime, once assigned)
		Global:      true,
		Scope:       w.file.Root,
	}
	if kind == KindGlobal {
		sym.Detail = "global " + sym.Name
	}
	w.file.Globals[sym.Name] = sym
	// Outline: attach to the scope where it was assigned.
	scope.Symbols = append(scope.Symbols, sym)
	return sym
}

// identifier records a reference unless the identifier is a field name or label.
func (w *walker) identifier(n *tree_sitter.Node, scope *Scope) {
	parent := n.Parent()
	if parent != nil {
		switch parent.Kind() {
		case "dot_index_expression":
			if f := parent.ChildByFieldName("field"); f != nil && f.StartByte() == n.StartByte() {
				return
			}
		case "method_index_expression":
			if f := parent.ChildByFieldName("method"); f != nil && f.StartByte() == n.StartByte() {
				return
			}
		case "field":
			// `{ x = 1 }` names a key; `{ [x] = 1 }` reads the variable x.
			if f := parent.ChildByFieldName("name"); f != nil && f.StartByte() == n.StartByte() && f.Kind() == "identifier" {
				if first := parent.Child(0); first == nil || first.Kind() != "[" {
					return
				}
			}
		case "goto_statement", "label_statement", "attribute", "parameters":
			return
		}
	}
	name := n.Utf8Text(w.file.Text)
	ref := Reference{Name: name, Start: int(n.StartByte()), End: int(n.EndByte()), Scope: scope}
	ref.Symbol = w.resolve(scope, name, ref.Start)
	if ref.Symbol == nil && scope.allowsImplicitGlobals(ref.Start) {
		ref.Symbol = w.file.Globals[name]
	}
	w.file.References = append(w.file.References, ref)
}

// fieldNameOf returns the field name under which n hangs off its parent, if any.
func fieldNameOf(n *tree_sitter.Node) string {
	parent := n.Parent()
	if parent == nil {
		return ""
	}
	for i := uint(0); i < parent.ChildCount(); i++ {
		c := parent.Child(i)
		if c.StartByte() == n.StartByte() && c.EndByte() == n.EndByte() && c.Kind() == n.Kind() {
			return parent.FieldNameForChild(uint32(i))
		}
	}
	return ""
}

// resolve walks the scope chain for a lexical declaration visible at `at`.
func (w *walker) resolve(scope *Scope, name string, at int) *Symbol {
	for s := scope; s != nil; s = s.Parent {
		for i := len(s.Symbols) - 1; i >= 0; i-- {
			sym := s.Symbols[i]
			if sym.Name == name && (!sym.Global || sym.ExplicitGlobal) && sym.Kind != KindField && sym.VisibleFrom <= at {
				return sym
			}
		}
	}
	return nil
}

// --- queries -------------------------------------------------------------------

// ScopeAt returns the innermost scope containing the offset.
func (f *File) ScopeAt(offset int) *Scope {
	s := f.Root
	for {
		var next *Scope
		for _, c := range s.Children {
			if c.Start <= offset && offset <= c.End {
				next = c
				break
			}
		}
		if next == nil {
			return s
		}
		s = next
	}
}

// SymbolAt returns the symbol referenced or declared at the offset.
func (f *File) SymbolAt(offset int) (*Symbol, *Reference) {
	for i := range f.References {
		r := &f.References[i]
		if r.Start <= offset && offset <= r.End {
			return r.Symbol, r
		}
	}
	var found *Symbol
	var visit func(s *Scope)
	visit = func(s *Scope) {
		for _, sym := range s.Symbols {
			if sym.Start <= offset && offset <= sym.End {
				found = sym
				return
			}
		}
		for _, c := range s.Children {
			if c.Start <= offset && offset <= c.End {
				visit(c)
			}
		}
	}
	visit(f.Root)
	return found, nil
}

// VisibleSymbols lists lexical declarations (innermost first), then implicit globals.
func (f *File) VisibleSymbols(offset int) []*Symbol {
	seen := map[string]bool{}
	var out []*Symbol
	for s := f.ScopeAt(offset); s != nil; s = s.Parent {
		for i := len(s.Symbols) - 1; i >= 0; i-- {
			sym := s.Symbols[i]
			if (sym.Global && !sym.ExplicitGlobal) || sym.Kind == KindField || sym.VisibleFrom > offset || seen[sym.Name] {
				continue
			}
			seen[sym.Name] = true
			out = append(out, sym)
		}
	}
	if !f.ScopeAt(offset).allowsImplicitGlobals(offset) {
		return out
	}
	names := make([]string, 0, len(f.Globals))
	for name := range f.Globals {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			out = append(out, f.Globals[name])
		}
	}
	return out
}

// Outline returns document symbols: functions nest their own locals and functions.
func (f *File) Outline() []*Symbol {
	return outline(f.Root)
}

func outline(s *Scope) []*Symbol {
	var out []*Symbol
	for _, sym := range s.Symbols {
		if sym.Kind == KindParameter {
			continue
		}
		out = append(out, sym)
	}
	for _, c := range s.Children {
		if c.Kind == ScopeFunction && c.Owner != nil {
			continue // reached through Symbol.Inner
		}
		out = append(out, outline(c)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// OutlineChildren returns the outline entries nested inside a function symbol.
func (f *File) OutlineChildren(sym *Symbol) []*Symbol {
	if sym.Inner == nil {
		return nil
	}
	return outline(sym.Inner)
}
