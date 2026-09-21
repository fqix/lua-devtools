package lsp

import (
	"errors"
	"os"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/fqix/lua-devtools/internal/analysis"
)

// referenceTarget classifies the name under the cursor with module resolution
// enabled. The release function must run once the target is no longer used.
func (s *Server) referenceTarget(doc *document, position protocol.Position) (*analysis.ReferenceTarget, int, func()) {
	f := doc.file
	f.ModuleMembers = s.moduleResolver(doc.uri, true)
	release := func() { f.ModuleMembers = nil; f.ResetReferences() }
	offset := f.OffsetOf(fromPosition(position))
	return f.ReferenceTargetAt(offset, doc.uri), offset, release
}

// siteLocation is one reference in one file.
type siteLocation struct {
	file workspaceFile
	rng  protocol.Range
}

// workspaceSites finds every site of a global or member across the workspace.
// It reports a conflict for rename when newName is given. A member whose
// table is declared in a file the scan does not cover (an installed package,
// a file outside the workspace) can be listed but not renamed: its
// declaration would stay behind.
func (s *Server) workspaceSites(doc *document, target *analysis.ReferenceTarget, declarations bool, newName string) ([]siteLocation, error) {
	needles := []string{target.Global}
	if target.Member != nil {
		needles = []string{target.Member.Name}
	}
	if newName != "" {
		needles = append(needles, newName)
	}
	declarationURI := ""
	if target.Declaration != nil {
		declarationURI = target.Declaration.URI
		if declarationURI == "" {
			declarationURI = doc.uri
		}
	}
	files := s.workspaceFiles(needles...)
	if target.Member != nil && target.Member.Owner != "" {
		covered := false
		for _, file := range files {
			if file.uri == target.Member.Owner {
				covered = true
				break
			}
		}
		if !covered {
			if newName != "" {
				return nil, errors.New("The member is declared outside the workspace sources; rename it where it is defined")
			}
			// Still show the declaring file's sites for references.
			if path := fileURIPath(target.Member.Owner); path != "" {
				if text, err := os.ReadFile(path); err == nil && len(text) <= scanMaxFile {
					files = append(files, workspaceFile{uri: target.Member.Owner, text: text})
				}
			}
		}
	}
	var out []siteLocation
	for _, file := range files {
		f, release := s.parse(file)
		var spans []analysis.Span
		switch {
		case target.Member != nil:
			if newName != "" && f.MemberDeclares(*target.Member, newName, file.uri) {
				release()
				return nil, errors.New("The new name conflicts with a declaration")
			}
			var ambiguous bool
			spans, ambiguous = f.MemberSites(*target.Member, file.uri)
			if ambiguous && newName != "" {
				release()
				return nil, errors.New("The table's member bindings cannot be determined statically after reassignment; rename is unsafe")
			}
			if !declarations && file.uri == declarationURI {
				declared := f.OffsetOf(target.Declaration.Start)
				kept := spans[:0]
				for _, span := range spans {
					if span.Start != declared {
						kept = append(kept, span)
					}
				}
				spans = kept
			}
		default:
			if newName != "" {
				if err := f.GlobalRenameConflict(target.Global, newName); err != nil {
					release()
					return nil, err
				}
			}
			spans = f.GlobalSites(target.Global, declarations)
		}
		for _, span := range spans {
			out = append(out, siteLocation{file: file, rng: toRange(f, span.Start, span.End)})
		}
		release()
	}
	return out, nil
}

func (s *Server) references(_ *glsp.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []protocol.Location{}
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return out, nil
	}
	target, offset, release := s.referenceTarget(doc, params.Position)
	defer release()
	switch {
	case target == nil:
		return out, nil
	case target.Symbol != nil:
		for _, span := range doc.file.SymbolReferences(offset, params.Context.IncludeDeclaration) {
			out = append(out, protocol.Location{URI: doc.uri, Range: toRange(doc.file, span.Start, span.End)})
		}
		return out, nil
	}
	sites, err := s.workspaceSites(doc, target, params.Context.IncludeDeclaration, "")
	if err != nil {
		return nil, err
	}
	for _, site := range sites {
		out = append(out, protocol.Location{URI: site.file.uri, Range: site.rng})
	}
	return out, nil
}

func (s *Server) prepareRename(_ *glsp.Context, params *protocol.PrepareRenameParams) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	target, _, release := s.referenceTarget(doc, params.Position)
	defer release()
	if target == nil {
		return nil, nil
	}
	name := target.Global
	switch {
	case target.Symbol != nil:
		if doc.file.RenameTarget(doc.file.OffsetOf(fromPosition(params.Position))) == nil {
			return nil, nil
		}
		name = target.Symbol.Name
	case target.Member != nil:
		name = target.Member.Name
	}
	if name == "_ENV" {
		return nil, nil
	}
	return protocol.RangeWithPlaceholder{Range: toRange(doc.file, target.Span.Start, target.Span.End), Placeholder: name}, nil
}

func (s *Server) rename(_ *glsp.Context, params *protocol.RenameParams) (*protocol.WorkspaceEdit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	target, offset, release := s.referenceTarget(doc, params.Position)
	defer release()
	if target == nil {
		return nil, errors.New("Only variables, globals and statically known table members can be renamed")
	}
	if target.Symbol != nil {
		spans, err := doc.file.Rename(offset, params.NewName)
		if err != nil {
			return nil, err
		}
		edits := []any{}
		for _, span := range spans {
			edits = append(edits, protocol.TextEdit{Range: toRange(doc.file, span.Start, span.End), NewText: params.NewName})
		}
		return &protocol.WorkspaceEdit{DocumentChanges: []any{documentEdit(doc.uri, &doc.version, edits)}}, nil
	}
	if err := analysis.ValidateRename(params.NewName); err != nil {
		return nil, err
	}
	current := target.Global
	if target.Member != nil {
		current = target.Member.Name
	}
	if params.NewName == current {
		return &protocol.WorkspaceEdit{DocumentChanges: []any{}}, nil
	}
	sites, err := s.workspaceSites(doc, target, true, params.NewName)
	if err != nil {
		return nil, err
	}
	changes := []any{}
	byURI := map[string]int{}
	for _, site := range sites {
		edit := protocol.TextEdit{Range: site.rng, NewText: params.NewName}
		index, ok := byURI[site.file.uri]
		if !ok {
			var version *int32
			if site.file.doc != nil {
				version = &site.file.doc.version
			}
			byURI[site.file.uri] = len(changes)
			changes = append(changes, documentEdit(site.file.uri, version, []any{edit}))
			continue
		}
		change := changes[index].(protocol.TextDocumentEdit)
		change.Edits = append(change.Edits, edit)
		changes[index] = change
	}
	return &protocol.WorkspaceEdit{DocumentChanges: changes}, nil
}

func documentEdit(uri string, version *int32, edits []any) protocol.TextDocumentEdit {
	return protocol.TextDocumentEdit{
		TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri}, Version: version},
		Edits:        edits,
	}
}

func (s *Server) signatureHelp(_ *glsp.Context, params *protocol.SignatureHelpParams) (*protocol.SignatureHelp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	doc.file.ModuleMembers = s.moduleResolver(doc.uri, true)
	defer func() { doc.file.ModuleMembers = nil }()
	signature := doc.file.SignatureAt(doc.file.OffsetOf(fromPosition(params.Position)))
	if signature == nil {
		return nil, nil
	}
	parameters := []protocol.ParameterInformation{}
	for _, parameter := range signature.Parameters {
		parameters = append(parameters, protocol.ParameterInformation{Label: parameter})
	}
	active := uint32(signature.Active)
	return &protocol.SignatureHelp{Signatures: []protocol.SignatureInformation{{Label: signature.Label, Parameters: parameters}}, ActiveParameter: &active}, nil
}
