package lsp

import (
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func (s *Server) references(_ *glsp.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []protocol.Location{}
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return out, nil
	}
	for _, span := range doc.file.SymbolReferences(doc.file.OffsetOf(fromPosition(params.Position)), params.Context.IncludeDeclaration) {
		out = append(out, protocol.Location{URI: doc.uri, Range: toRange(doc.file, span.Start, span.End)})
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
	offset := doc.file.OffsetOf(fromPosition(params.Position))
	symbol := doc.file.RenameTarget(offset)
	if symbol == nil {
		return nil, nil
	}
	start, end := symbol.Start, symbol.End
	if _, ref := doc.file.SymbolAt(offset); ref != nil {
		start, end = ref.Start, ref.End
	}
	return protocol.RangeWithPlaceholder{Range: toRange(doc.file, start, end), Placeholder: symbol.Name}, nil
}

func (s *Server) rename(_ *glsp.Context, params *protocol.RenameParams) (*protocol.WorkspaceEdit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	spans, err := doc.file.Rename(doc.file.OffsetOf(fromPosition(params.Position)), params.NewName)
	if err != nil {
		return nil, err
	}
	edits := []any{}
	for _, span := range spans {
		edits = append(edits, protocol.TextEdit{Range: toRange(doc.file, span.Start, span.End), NewText: params.NewName})
	}
	return &protocol.WorkspaceEdit{DocumentChanges: []any{protocol.TextDocumentEdit{
		TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: doc.uri}, Version: &doc.version},
		Edits:        edits,
	}}}, nil
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
