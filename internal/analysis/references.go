package analysis

import (
	"errors"
	"sort"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// identityState is a whole-file replay of member writes that ignores scope, so
// every site naming a field of the same table shares one pointer and every
// field declaration is known wherever it is written. Which table a name is
// bound to at a given site is position-dependent (see bindingState), so that
// part is replayed per site instead. Table constructors are cached (see
// File.constructorValues) so the pointers agree across replays, which is why
// the state must be reset before scoped queries run again.
type identityState struct {
	values   map[*Symbol]*memberValue
	export   *memberValue // the table the chunk returns, when static
	rebinds  []memberWrite
	bindings map[int]map[*Symbol]*memberValue // per rebinding count, see bindingState
}

func (f *File) identityState() *identityState {
	if f.identities != nil {
		return f.identities
	}
	f.constructorValues = map[uintptr]*memberValue{}
	f.writeValues = map[writeKey]*memberValue{}
	f.rootPlaceholders = map[*Symbol]*memberValue{}
	type binding struct {
		scope *Scope
		value *memberValue
	}
	bindings := map[*Symbol][]binding{}
	state := &identityState{bindings: map[int]map[*Symbol]*memberValue{}}
	state.values = f.replayMemberWrites(func(memberWrite) bool { return true }, func(write memberWrite, before, after *memberValue) {
		if len(write.path) == 0 {
			bindings[write.root] = append(bindings[write.root], binding{write.scope, after})
		} else if before != nil && before != after {
			// Field histories share mutable table shapes. Until we can represent
			// each version, refuse renames of either replaced table's members.
			before.markRenameUnsafe()
			after.markRenameUnsafe()
		}
	})
	for _, writes := range bindings {
		if len(writes) < 2 {
			continue
		}
		uncertain := false
		for _, write := range writes {
			uncertain = uncertain || write.scope != writes[0].scope
			for scope := write.scope; scope != nil; scope = scope.Parent {
				uncertain = uncertain || scope.Loop
			}
		}
		if uncertain {
			// Mark every candidate, not only the source-order winner. Aliases
			// retain these pointers even when they have no rebindings themselves.
			for _, write := range writes {
				write.value.markRenameUnsafe()
			}
		}
	}
	// Bindings change at whole-name writes and at the first field write of a
	// name that was never bound (which creates its placeholder table).
	writes := append([]memberWrite(nil), f.memberWrites...)
	sort.SliceStable(writes, func(i, j int) bool { return writes[i].at < writes[j].at })
	bound := map[*Symbol]bool{}
	for _, write := range writes {
		if len(write.path) == 0 || !bound[write.root] {
			state.rebinds = append(state.rebinds, write)
		}
		bound[write.root] = true
	}
	root := f.tree.RootNode()
	for i := uint(0); i < root.NamedChildCount(); i++ {
		node := root.NamedChild(i)
		if node.Kind() != "return_statement" {
			continue
		}
		if list := node.NamedChild(0); list != nil && list.NamedChildCount() == 1 {
			state.export = f.inferMemberValue(list.NamedChild(0), state.values, 0)
		}
		break
	}
	f.identities = state
	return state
}

// ResetReferences drops the cached identity state. Callers must reset before
// running scoped member queries (completion, definition) on the same parse.
func (f *File) ResetReferences() {
	f.identities = nil
	f.constructorValues = nil
	f.writeValues = nil
	f.rootPlaceholders = nil
}

func (v *memberValue) markRenameUnsafe() {
	if v == nil || v.renameUnsafe {
		return
	}
	v.renameUnsafe = true
	for _, field := range v.fields {
		field.markRenameUnsafe()
	}
	v.index.markRenameUnsafe()
}

// MemberIdentity names one field of one table. Owner is the URI of the file
// whose chunk returns the table, so identities compare across files; without
// an owner the table is comparable only within the parse that produced it.
type MemberIdentity struct {
	Owner string
	Name  string
	table *memberValue
}

func (m MemberIdentity) equals(o MemberIdentity) bool {
	if m.Name != o.Name {
		return false
	}
	if m.Owner != "" || o.Owner != "" {
		return m.Owner == o.Owner
	}
	return m.table == o.table
}

// ReferenceTarget is what the cursor names: a lexical symbol, a global, or a
// table member. Span is the name token under the cursor.
type ReferenceTarget struct {
	Symbol      *Symbol
	Global      string
	Member      *MemberIdentity
	Declaration *Definition // where the member is declared; URI is empty for this file
	Span        Span
}

// memberSite is one static field access, write target or constructor key.
type memberSite struct {
	table *tree_sitter.Node // expression the field is read from, or the table constructor
	name  string
	span  Span
}

func (f *File) memberSiteOf(n *tree_sitter.Node) *memberSite {
	parent := n.Parent()
	if parent == nil {
		return nil
	}
	switch parent.Kind() {
	case "dot_index_expression", "bracket_index_expression", "method_index_expression":
		key := lastKeyNode(parent)
		if key == nil || key.StartByte() != n.StartByte() {
			return nil
		}
		name := f.staticKey(key, parent.Kind() != "bracket_index_expression")
		if name == "" {
			return nil
		}
		start, end := f.keySpan(key)
		return &memberSite{table: parent.ChildByFieldName("table"), name: name, span: Span{start, end}}
	case "field":
		key := parent.ChildByFieldName("name")
		if key == nil || key.StartByte() != n.StartByte() {
			return nil
		}
		first := parent.Child(0)
		name := f.staticKey(key, first != nil && first.Kind() != "[")
		if name == "" {
			return nil
		}
		constructor := parent.Parent()
		if constructor == nil || constructor.Kind() != "table_constructor" {
			return nil
		}
		start, end := f.keySpan(key)
		return &memberSite{table: constructor, name: name, span: Span{start, end}}
	}
	return nil
}

// memberSites visits every static site naming the field.
func (f *File) memberSites(name string, visit func(memberSite)) {
	var walk func(n *tree_sitter.Node)
	walk = func(n *tree_sitter.Node) {
		if n.Kind() == "identifier" || n.Kind() == "string" {
			if site := f.memberSiteOf(n); site != nil && site.name == name {
				visit(*site)
			}
		}
		cursor := n.Walk()
		children := n.NamedChildren(cursor)
		cursor.Close()
		for i := range children {
			walk(&children[i])
		}
	}
	walk(f.tree.RootNode())
}

// bindingState is the replay of every write up to the offset, so a name
// resolves to the table it was last bound to before the site in source order.
// States only change at rebindings of whole names, so they are cached per
// count of preceding rebindings.
func (f *File) bindingState(offset int, state *identityState) map[*Symbol]*memberValue {
	count := sort.Search(len(state.rebinds), func(i int) bool { return state.rebinds[i].at > offset })
	if cached := state.bindings[count]; cached != nil {
		return cached
	}
	values := f.replayMembers(func(write memberWrite) bool { return write.at <= offset })
	state.bindings[count] = values
	return values
}

// siteTable resolves the table a site reads its field from, using the binding
// in force at the site. ambiguous reports that the site's root name is rebound
// somewhere the binding cannot be decided statically: in a branch or function
// that does not enclose the site, or later inside a loop the site is in. The
// candidates are the tables every rebinding of that name produces, so callers
// can tell whether the ambiguity could involve a given table.
func (f *File) siteTable(site memberSite, state *identityState) (table *memberValue, candidates []*memberValue, ambiguous bool) {
	var sym *Symbol
	if root, _ := f.memberPath(site.table); root != nil {
		sym, _ = f.SymbolAt(int(root.StartByte()))
	}
	var binding *memberWrite
	if sym != nil {
		for i := range state.rebinds {
			write := &state.rebinds[i]
			if write.root == sym && len(write.path) == 0 && write.at <= site.span.Start {
				binding = write
			}
		}
	}
	if binding == nil {
		// No name bound before the site (a parameter, upvalue or global written
		// only through fields, or no name at all): the whole-file state holds
		// the one placeholder table such a name gets.
		return f.inferMemberValue(site.table, state.values, 0), nil, false
	}
	table = f.inferMemberValue(site.table, f.bindingState(site.span.Start, state), 0)
	siteScope := f.ScopeAt(site.span.Start)
	if !binding.scope.Encloses(siteScope) {
		ambiguous = true
	}
	for i := range state.rebinds {
		write := &state.rebinds[i]
		if write.root != sym || len(write.path) > 0 || write == binding {
			continue
		}
		candidates = append(candidates, f.bindingState(write.at, state)[sym])
		if ambiguous {
			continue
		}
		switch {
		case !functionScope(write.scope).Encloses(siteScope):
			// Rebound inside a function that may run at any time.
			ambiguous = true
		case write.at > site.span.Start && functionScope(siteScope) != functionScope(write.scope):
			// The site is in a closure that may run after the later rebinding.
			ambiguous = true
		case write.at > site.span.Start && loopEnclosingBoth(siteScope, write.scope):
			ambiguous = true
		}
	}
	return table, candidates, ambiguous
}

// functionScope is the function (or chunk) whose body contains the scope.
func functionScope(s *Scope) *Scope {
	for ; s.Parent != nil; s = s.Parent {
		if s.Kind == ScopeFunction {
			return s
		}
	}
	return s
}

func loopEnclosingBoth(a, b *Scope) bool {
	for s := a; s != nil; s = s.Parent {
		if s.Loop && s.Encloses(b) {
			return true
		}
	}
	return false
}

// siteOwner is the table in the site's __index chain that declares the field.
func (f *File) siteOwner(site memberSite, state *identityState) *memberValue {
	table, _, _ := f.siteTable(site, state)
	return table.owner(site.name, map[*memberValue]bool{})
}

func (f *File) identityOf(owner *memberValue, name, self string, state *identityState) MemberIdentity {
	identity := MemberIdentity{Name: name, table: owner}
	if owner.origin != "" {
		identity.Owner = owner.origin
	} else if owner == state.export {
		identity.Owner = self
	}
	return identity
}

func (f *File) nodeAt(offset int) *tree_sitter.Node {
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
	// A quoted key is the string node, not its content or delimiter children.
	for n != nil && n.Kind() != "string" && n.Parent() != nil && n.Parent().Kind() == "string" {
		n = n.Parent()
	}
	return n
}

// ReferenceTargetAt classifies the name under the cursor. self is this file's
// URI, used to identify members of the table it returns.
func (f *File) ReferenceTargetAt(offset int, self string) *ReferenceTarget {
	n := f.nodeAt(offset)
	if n == nil {
		return nil
	}
	if site := f.memberSiteOf(n); site != nil {
		state := f.identityState()
		owner := f.siteOwner(*site, state)
		if owner == nil {
			return nil
		}
		identity := f.identityOf(owner, site.name, self, state)
		return &ReferenceTarget{Member: &identity, Declaration: owner.decls[site.name], Span: site.span}
	}
	if n.Kind() != "identifier" {
		return nil
	}
	symbol, ref := f.SymbolAt(offset)
	span := Span{int(n.StartByte()), int(n.EndByte())}
	switch {
	case symbol != nil && symbol.Kind == KindField:
		return nil
	case symbol != nil && !symbol.Global:
		return &ReferenceTarget{Symbol: symbol, Span: span}
	case symbol != nil:
		return &ReferenceTarget{Global: symbol.Name, Span: span}
	case ref != nil:
		return &ReferenceTarget{Global: ref.Name, Span: span}
	}
	return nil
}

// MemberSites lists the spans of every site that names the member in this
// file, using the binding in force at each site. ambiguous is set when a site
// of that name might refer to the member but its table cannot be decided
// statically; references still list such sites by their source-order binding,
// rename must refuse.
func (f *File) MemberSites(identity MemberIdentity, self string) (spans []Span, ambiguous bool) {
	state := f.identityState()
	spans = []Span{}
	f.memberSites(identity.Name, func(site memberSite) {
		table, candidates, undecided := f.siteTable(site, state)
		matches := func(t *memberValue) bool {
			owner := t.owner(site.name, map[*memberValue]bool{})
			match := owner != nil && identity.equals(f.identityOf(owner, site.name, self, state))
			if match && owner.renameUnsafe {
				ambiguous = true
			}
			return match
		}
		if matches(table) {
			spans = append(spans, site.span)
			ambiguous = ambiguous || undecided
			return
		}
		if undecided {
			for _, candidate := range candidates {
				if matches(candidate) {
					ambiguous = true
					break
				}
			}
		}
	})
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	return spans, ambiguous
}

// MemberDeclares reports whether the member's table (or a table it inherits
// from through __index) already declares another field in this file.
func (f *File) MemberDeclares(identity MemberIdentity, name, self string) bool {
	state := f.identityState()
	found := false
	f.memberSites(identity.Name, func(site memberSite) {
		if found {
			return
		}
		table, candidates, _ := f.siteTable(site, state)
		for _, t := range append([]*memberValue{table}, candidates...) {
			if owner := t.owner(identity.Name, map[*memberValue]bool{}); owner != nil && identity.equals(f.identityOf(owner, site.name, self, state)) {
				found = t.owner(name, map[*memberValue]bool{}) != nil
				if found {
					return
				}
			}
		}
	})
	return found
}

// MemberDeclarationAt locates where the member under the cursor is declared.
// URI is empty for this file.
func (f *File) MemberDeclarationAt(offset int) *Definition {
	n := f.nodeAt(offset)
	if n == nil {
		return nil
	}
	site := f.memberSiteOf(n)
	if site == nil {
		return nil
	}
	owner := f.siteOwner(*site, f.identityState())
	if owner == nil {
		return nil
	}
	return owner.decls[site.name]
}

// GlobalSites lists uses of a global name in this file, with its declarations
// (first assignments, `function name()`, Lua 5.5 `global` statements) when asked.
func (f *File) GlobalSites(name string, declarations bool) []Span {
	spans := []Span{}
	var visit func(s *Scope)
	visit = func(s *Scope) {
		for _, sym := range s.Symbols {
			if sym.Global && sym.Name == name {
				spans = append(spans, Span{sym.Start, sym.End})
			}
		}
		for _, c := range s.Children {
			visit(c)
		}
	}
	if declarations {
		visit(f.Root)
	}
	for _, ref := range f.References {
		if ref.Name == name && (ref.Symbol == nil || ref.Symbol.Global) {
			spans = append(spans, Span{ref.Start, ref.End})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	return spans
}

// GlobalRenameConflict reports why renaming the global would change bindings
// in this file: a lexical name would capture the new name, or another global
// already uses it.
func (f *File) GlobalRenameConflict(name, newName string) error {
	w := walker{file: f}
	if len(f.GlobalSites(newName, true)) > 0 {
		return errors.New("The new name conflicts with a declaration")
	}
	for _, span := range f.GlobalSites(name, true) {
		scope := f.ScopeAt(span.Start)
		// A function's name is assigned in its enclosing scope, not the
		// function body (whose parameters must not count as captures).
		if symbol, _ := f.SymbolAt(span.Start); symbol != nil && symbol.Start == span.Start && scope.Kind == ScopeFunction && scope.Owner == symbol {
			scope = scope.Parent
		}
		if w.resolve(scope, newName, span.Start) != nil {
			return errors.New("The new name would change another binding")
		}
	}
	return nil
}

// ValidateRename rejects names that cannot be identifiers.
func ValidateRename(name string) error {
	if !IsIdentifier(name) || name == "_ENV" {
		return errors.New("Invalid Lua identifier")
	}
	return nil
}
