package analysis

import (
	"errors"
	"sort"
)

type Span struct{ Start, End int }

// SymbolReferences follows lexical bindings, keeping shadowed names separate.
func (f *File) SymbolReferences(offset int, declaration bool) []Span {
	symbol, _ := f.SymbolAt(offset)
	spans := []Span{}
	if symbol == nil || symbol.Kind == KindField {
		return spans
	}
	if declaration {
		spans = append(spans, Span{Start: symbol.Start, End: symbol.End})
	}
	for _, ref := range f.References {
		if ref.Symbol == symbol {
			spans = append(spans, Span{Start: ref.Start, End: ref.End})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	return spans
}

func (f *File) RenameTarget(offset int) *Symbol {
	symbol, _ := f.SymbolAt(offset)
	if symbol == nil || symbol.Global || symbol.Kind == KindField || symbol.Name == "_ENV" {
		return nil
	}
	return symbol
}

// Rename checks both capture of renamed references and capture of existing names.
func (f *File) Rename(offset int, name string) ([]Span, error) {
	symbol := f.RenameTarget(offset)
	if symbol == nil {
		return nil, errors.New("Only lexical variables, parameters and functions can be renamed")
	}
	if !IsIdentifier(name) || name == "_ENV" {
		return nil, errors.New("Invalid Lua identifier")
	}
	if name == symbol.Name {
		return []Span{}, nil
	}
	for _, sibling := range symbol.Scope.Symbols {
		if sibling != symbol && sibling.Name == name {
			return nil, errors.New("The new name conflicts with a declaration")
		}
	}
	old := symbol.Name
	symbol.Name = name
	defer func() { symbol.Name = old }()
	w := walker{file: f}
	for _, ref := range f.References {
		resolved := w.resolve(ref.Scope, name, ref.Start)
		if (ref.Symbol == symbol && resolved != symbol) || (ref.Symbol != symbol && ref.Name == name && resolved == symbol) {
			return nil, errors.New("The new name would change another binding")
		}
	}
	return f.SymbolReferences(offset, true), nil
}
