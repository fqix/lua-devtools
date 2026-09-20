// Package lsp implements the Lua language server on top of tliron/glsp.
// Documents are parsed with internal/analysis on every change; requests answer
// from the latest parse.
package lsp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"github.com/tliron/glsp/server"

	"github.com/fqix/lua-devtools/internal/analysis"
	"github.com/fqix/lua-devtools/internal/i18n"
)

const (
	Name             = "lua-devtools"
	diagnosticsDelay = 200 * time.Millisecond
)

// Version is set at build time from vscode/package.json (see scripts/build-go.mjs).
var Version = "dev"

type document struct {
	uri     string
	version int32
	text    []byte
	file    *analysis.File
	timer   *time.Timer
}

type Server struct {
	mu             sync.Mutex
	docs           map[string]*document
	locale         string // normalized client locale from initialize
	runtime        *interpreter
	workspaceRoots []string
}

func (s *Server) t(key string, args ...any) string {
	return i18n.T(s.locale, key, args...)
}

// Run serves LSP over stdio until the client exits.
func Run(debug bool) error {
	s := &Server{docs: map[string]*document{}, locale: "en"}
	handler := protocol.Handler{
		Initialize:                 s.initialize,
		Initialized:                func(*glsp.Context, *protocol.InitializedParams) error { return nil },
		Shutdown:                   func(*glsp.Context) error { return nil },
		SetTrace:                   func(*glsp.Context, *protocol.SetTraceParams) error { return nil },
		TextDocumentDidOpen:        s.didOpen,
		TextDocumentDidChange:      s.didChange,
		TextDocumentDidClose:       s.didClose,
		TextDocumentDocumentSymbol: s.documentSymbol,
		TextDocumentDefinition:     s.definition,
		TextDocumentCompletion:     s.completion,
		TextDocumentHover:          s.hover,
		TextDocumentCodeLens:       s.codeLens,
	}
	srv := server.NewServer(&handler, Name, debug)
	return srv.RunStdio()
}

func (s *Server) initialize(_ *glsp.Context, params *protocol.InitializeParams) (any, error) {
	if params.Locale != nil {
		s.locale = i18n.Normalize(*params.Locale)
	}
	var options struct {
		LuaPath        string   `json:"luaPath"`
		UseInterpreter bool     `json:"useInterpreter"`
		WorkspaceRoots []string `json:"workspaceRoots"`
	}
	if raw, err := json.Marshal(params.InitializationOptions); err == nil {
		_ = json.Unmarshal(raw, &options)
	}
	s.workspaceRoots = options.WorkspaceRoots
	if options.UseInterpreter {
		s.runtime = inspectInterpreter(options.LuaPath)
	}
	caps := protocol.ServerCapabilities{
		TextDocumentSync:       protocol.TextDocumentSyncKindIncremental,
		DocumentSymbolProvider: true,
		DefinitionProvider:     true,
		HoverProvider:          true,
		CompletionProvider:     &protocol.CompletionOptions{TriggerCharacters: []string{".", ":"}},
		CodeLensProvider:       &protocol.CodeLensOptions{},
	}
	version := Version
	return protocol.InitializeResult{
		Capabilities: caps,
		ServerInfo:   &protocol.InitializeResultServerInfo{Name: Name, Version: &version},
	}, nil
}

// --- document sync ---------------------------------------------------------------

func (s *Server) didOpen(ctx *glsp.Context, params *protocol.DidOpenTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := &document{uri: params.TextDocument.URI, version: params.TextDocument.Version, text: []byte(params.TextDocument.Text)}
	s.docs[doc.uri] = doc
	s.reparse(ctx, doc)
	return nil
}

func (s *Server) didChange(ctx *glsp.Context, params *protocol.DidChangeTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil
	}
	for _, change := range params.ContentChanges {
		switch c := change.(type) {
		case protocol.TextDocumentContentChangeEventWhole:
			doc.text = []byte(c.Text)
		case protocol.TextDocumentContentChangeEvent:
			if c.Range == nil {
				doc.text = []byte(c.Text)
				continue
			}
			start := offsetAt(doc.text, c.Range.Start)
			end := offsetAt(doc.text, c.Range.End)
			if end < start {
				end = start
			}
			next := make([]byte, 0, len(doc.text)-(end-start)+len(c.Text))
			next = append(next, doc.text[:start]...)
			next = append(next, c.Text...)
			next = append(next, doc.text[end:]...)
			doc.text = next
		}
	}
	doc.version = params.TextDocument.Version
	s.reparse(ctx, doc)
	return nil
}

func (s *Server) didClose(ctx *glsp.Context, params *protocol.DidCloseTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil
	}
	if doc.timer != nil {
		doc.timer.Stop()
	}
	if doc.file != nil {
		doc.file.Close()
	}
	delete(s.docs, doc.uri)
	// Clear the diagnostics VS Code still shows for the closed file.
	ctx.Notify(protocol.ServerTextDocumentPublishDiagnostics, protocol.PublishDiagnosticsParams{
		URI: doc.uri, Diagnostics: []protocol.Diagnostic{},
	})
	return nil
}

// reparse must be called with s.mu held. Parsing is immediate (later requests
// need it); publishing diagnostics is debounced so typing does not flicker.
func (s *Server) reparse(ctx *glsp.Context, doc *document) {
	if doc.file != nil {
		doc.file.Close()
	}
	doc.file = analysis.Parse(doc.text)

	if doc.timer != nil {
		doc.timer.Stop()
	}
	version := doc.version
	text := append([]byte(nil), doc.text...)
	fallback := s.diagnostics(doc)
	runtime := s.runtime
	doc.timer = time.AfterFunc(diagnosticsDelay, func() {
		result := fallback
		if runtime != nil {
			if diagnostics, ok := runtime.syntaxDiagnostics(text); ok {
				result.Diagnostics = diagnostics
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		current := s.docs[doc.uri]
		if current != doc || current.version != version || current.file == nil {
			return
		}
		ctx.Notify(protocol.ServerTextDocumentPublishDiagnostics, result)
	})
}

func (s *Server) diagnostics(doc *document) protocol.PublishDiagnosticsParams {
	f := doc.file
	out := make([]protocol.Diagnostic, 0, len(f.Diagnostics))
	severity := protocol.DiagnosticSeverityError
	source := Name
	for _, d := range f.Diagnostics {
		arg := d.Arg
		if d.Key == "diagnostic.unexpected" && arg == "" {
			arg = s.t("diagnostic.endOfInput")
		}
		message := s.t(d.Key, arg)
		if d.Key == "diagnostic.unclosed" {
			message = s.t(d.Key, arg, d.Construct, d.OpeningLine)
		}
		out = append(out, protocol.Diagnostic{
			Range:    toRange(f, d.Start, d.End),
			Severity: &severity,
			Source:   &source,
			Message:  message,
		})
	}
	version := uint32(doc.version)
	return protocol.PublishDiagnosticsParams{URI: doc.uri, Version: &version, Diagnostics: out}
}

// --- language features ----------------------------------------------------------

func (s *Server) documentSymbol(_ *glsp.Context, params *protocol.DocumentSymbolParams) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	return toDocumentSymbols(doc.file, doc.file.Outline()), nil
}

func toDocumentSymbols(f *analysis.File, syms []*analysis.Symbol) []protocol.DocumentSymbol {
	out := make([]protocol.DocumentSymbol, 0, len(syms))
	for _, sym := range syms {
		kind := protocol.SymbolKindVariable
		switch sym.Kind {
		case analysis.KindFunction:
			kind = protocol.SymbolKindFunction
		case analysis.KindField:
			kind = protocol.SymbolKindMethod
		}
		detail := sym.Detail
		ds := protocol.DocumentSymbol{
			Name:           sym.Name,
			Detail:         &detail,
			Kind:           kind,
			Range:          toRange(f, sym.FullStart, sym.FullEnd),
			SelectionRange: toRange(f, sym.Start, sym.End),
		}
		if children := f.OutlineChildren(sym); len(children) > 0 {
			ds.Children = toDocumentSymbols(f, children)
		}
		out = append(out, ds)
	}
	return out
}

func (s *Server) definition(_ *glsp.Context, params *protocol.DefinitionParams) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	f := doc.file
	f.ModuleMembers = s.moduleResolver(params.TextDocument.URI, true)
	defer func() { f.ModuleMembers = nil }()
	if def := f.ImplementationAt(f.OffsetOf(fromPosition(params.Position))); def != nil {
		uri := def.URI
		if uri == "" {
			uri = doc.uri
		}
		return protocol.Location{URI: uri, Range: protocol.Range{Start: toPosition(def.Start), End: toPosition(def.End)}}, nil
	}
	sym, _ := f.SymbolAt(f.OffsetOf(fromPosition(params.Position)))
	if sym == nil {
		return nil, nil
	}
	return protocol.Location{URI: doc.uri, Range: toRange(f, sym.Start, sym.End)}, nil
}

func (s *Server) hover(_ *glsp.Context, params *protocol.HoverParams) (*protocol.Hover, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	f := doc.file
	offset := f.OffsetOf(fromPosition(params.Position))
	sym, ref := f.SymbolAt(offset)

	var text string
	var rng protocol.Range
	switch {
	case sym != nil:
		line := f.PositionOf(sym.Start).Line + 1
		text = fmt.Sprintf("```lua\n%s\n```\n%s", sym.Detail, s.t("hover.definedOnLine", line))
		if ref != nil {
			rng = toRange(f, ref.Start, ref.End)
		} else {
			rng = toRange(f, sym.Start, sym.End)
		}
	case ref != nil:
		if doc, ok := s.builtinDoc(ref.Name); ok {
			text = fmt.Sprintf("```lua\n%s\n```\n%s", ref.Name, doc)
		} else {
			text = fmt.Sprintf("```lua\nglobal %s\n```\n%s", ref.Name, s.t("hover.globalUndefined"))
		}
		rng = toRange(f, ref.Start, ref.End)
	default:
		return nil, nil
	}
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.MarkupKindMarkdown, Value: text},
		Range:    &rng,
	}, nil
}

func (s *Server) completion(_ *glsp.Context, params *protocol.CompletionParams) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	f := doc.file
	offset := f.OffsetOf(fromPosition(params.Position))

	if f.InStringOrComment(offset) {
		return protocol.CompletionList{Items: []protocol.CompletionItem{}}, nil
	}
	if receiver, method, ok := memberReceiver(f.Text, offset); ok {
		// A dangling member access can make tree-sitter discard its enclosing
		// function, hiding parameters and locals. Complete it in a temporary
		// parse so lexical shadowing survives while the user is typing.
		if len(f.Diagnostics) > 0 && len(receiver) > 0 {
			repaired := append([]byte{}, f.Text[:offset]...)
			repaired = append(repaired, []byte("__lua_devtools_completion()")...)
			repaired = append(repaired, f.Text[offset:]...)
			candidate := analysis.Parse(repaired)
			defer candidate.Close()
			if len(candidate.Diagnostics) == 0 {
				f = candidate
			}
		}
		f.ModuleMembers = s.moduleResolver(params.TextDocument.URI)
		defer func() { f.ModuleMembers = nil }()
		return memberCompletions(f, offset, receiver, method, s.runtime), nil
	}

	var items []protocol.CompletionItem
	seen := map[string]bool{}
	add := func(label string, kind protocol.CompletionItemKind, detail string) {
		if seen[label] {
			return
		}
		seen[label] = true
		k := kind
		d := detail
		items = append(items, protocol.CompletionItem{Label: label, Kind: &k, Detail: &d})
	}
	for _, sym := range f.VisibleSymbols(offset) {
		kind := protocol.CompletionItemKindVariable
		if sym.Kind == analysis.KindFunction {
			kind = protocol.CompletionItemKindFunction
		}
		add(sym.Name, kind, sym.Detail)
	}
	names := make([]string, 0, len(builtinDocs))
	for name := range builtinDocs {
		if _, ok := s.builtinDoc(name); !ok {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		kind := protocol.CompletionItemKindFunction
		if strings.HasPrefix(builtinDocs[name], "library") {
			kind = protocol.CompletionItemKindModule
		}
		add(name, kind, builtinDocs[name])
	}
	if s.runtime == nil || s.runtime.globals["warn"] {
		add("warn", protocol.CompletionItemKindFunction, "warn(message, ...)")
	}
	if s.runtime != nil && s.runtime.version == "Lua 5.5" {
		add("global", protocol.CompletionItemKindKeyword, s.t("completion.keyword"))
	}
	for _, kw := range luaKeywords {
		add(kw, protocol.CompletionItemKindKeyword, s.t("completion.keyword"))
	}
	return protocol.CompletionList{IsIncomplete: false, Items: items}, nil
}

// Commands the VS Code extension registers; the server only names them.
const (
	CommandRun       = "luaDevtools.run"
	CommandDebug     = "luaDevtools.debug"
	CommandRunTest   = "luaDevtools.runTest"
	CommandDebugTest = "luaDevtools.debugTest"
)

// codeLens puts "Run" and "Debug" actions on the first line of every Lua file.
// The client executes the commands, since starting a debug session is an editor
// concern, and the file URI travels along as the argument.
func (s *Server) codeLens(_ *glsp.Context, params *protocol.CodeLensParams) ([]protocol.CodeLens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		return nil, nil
	}
	first := protocol.Range{Start: protocol.Position{Line: 0, Character: 0}, End: protocol.Position{Line: 0, Character: 0}}
	lenses := []protocol.CodeLens{
		{Range: first, Command: &protocol.Command{Title: s.t("codelens.run"), Command: CommandRun, Arguments: []any{doc.uri}}},
		{Range: first, Command: &protocol.Command{Title: s.t("codelens.debug"), Command: CommandDebug, Arguments: []any{doc.uri}}},
	}
	appendTests := func(tests []analysis.TestCase, framework string) {
		for _, test := range tests {
			position := toPosition(doc.file.PositionOf(test.Start))
			span := protocol.Range{Start: position, End: position}
			lenses = append(lenses,
				protocol.CodeLens{Range: span, Command: &protocol.Command{Title: s.t("codelens.runTest"), Command: CommandRunTest, Arguments: []any{doc.uri, test.Name, framework}}},
				protocol.CodeLens{Range: span, Command: &protocol.Command{Title: s.t("codelens.debugTest"), Command: CommandDebugTest, Arguments: []any{doc.uri, test.Name, framework}}},
			)
		}
	}
	appendTests(doc.file.LuaUnitTests(), "luaunit")
	appendTests(doc.file.BustedTests(), "busted")
	return lenses, nil
}

// --- helpers ------------------------------------------------------------------------

func toRange(f *analysis.File, start, end int) protocol.Range {
	return protocol.Range{Start: toPosition(f.PositionOf(start)), End: toPosition(f.PositionOf(end))}
}

func toPosition(p analysis.Position) protocol.Position {
	return protocol.Position{Line: uint32(p.Line), Character: uint32(p.Character)}
}

func fromPosition(p protocol.Position) analysis.Position {
	return analysis.Position{Line: int(p.Line), Character: int(p.Character)}
}

// offsetAt converts an LSP position (UTF-16 columns) to a byte offset in text.
func offsetAt(text []byte, pos protocol.Position) int {
	line := 0
	i := 0
	for line < int(pos.Line) && i < len(text) {
		if text[i] == '\n' {
			line++
		}
		i++
	}
	units := 0
	for i < len(text) && units < int(pos.Character) {
		r, size := utf8.DecodeRune(text[i:])
		if r == '\n' {
			break
		}
		if r >= 0x10000 {
			units += 2
		} else {
			units++
		}
		i += size
	}
	return i
}

var luaKeywords = []string{
	"and", "break", "do", "else", "elseif", "end", "false", "for", "function", "goto", "if", "in",
	"local", "nil", "not", "or", "repeat", "return", "then", "true", "until", "while",
}

// builtinDoc follows the interpreter's actual globals, with a Lua 5.4 fallback.
func (s *Server) builtinDoc(name string) (string, bool) {
	if s.runtime != nil && !s.runtime.globals[name] {
		return "", false
	}
	if s.runtime == nil && (name == "bit32" || name == "loadstring" || name == "unpack" ||
		name == "setfenv" || name == "getfenv" || name == "module" || name == "newproxy" || name == "bit" || name == "jit") {
		return "", false
	}
	doc, ok := builtinDocs[name]
	return doc, ok
}

// builtinDocs describes globals across supported Lua versions; builtinDoc filters availability.
var builtinDocs = map[string]string{
	"getfenv":        "getfenv([f]) returns a function's environment (Lua 5.1)",
	"setfenv":        "setfenv(f, table) sets a function's environment (Lua 5.1)",
	"module":         "module(name [, ...]) creates a module (Lua 5.1)",
	"newproxy":       "newproxy([boolean or proxy]) creates a userdata proxy (Lua 5.1)",
	"bit":            "library: bitwise operations (LuaJIT)",
	"jit":            "library: JIT compiler control (LuaJIT)",
	"bit32":          "library: bitwise operations (Lua 5.2)",
	"loadstring":     "loadstring(string [, chunkname]) compiles a Lua chunk",
	"unpack":         "unpack(list [, i [, j]]) returns elements of a list",
	"assert":         "assert(v [, message]) raises an error if v is false or nil",
	"collectgarbage": "collectgarbage([opt [, arg]]) controls the garbage collector",
	"dofile":         "dofile([filename]) runs a Lua file",
	"error":          "error(message [, level]) raises an error",
	"getmetatable":   "getmetatable(object) returns the metatable",
	"ipairs":         "ipairs(t) iterates array part in order",
	"load":           "load(chunk [, chunkname [, mode [, env]]]) compiles a chunk",
	"loadfile":       "loadfile([filename [, mode [, env]]]) compiles a file",
	"next":           "next(table [, index]) iterates a table",
	"pairs":          "pairs(t) iterates all key/value pairs",
	"pcall":          "pcall(f [, args...]) calls f in protected mode",
	"print":          "print(...) writes values to stdout",
	"rawequal":       "rawequal(v1, v2) compares without metamethods",
	"rawget":         "rawget(table, index) indexes without metamethods",
	"rawlen":         "rawlen(v) length without metamethods",
	"rawset":         "rawset(table, index, value) assigns without metamethods",
	"require":        "require(modname) loads a module",
	"select":         "select(index, ...) returns arguments after index",
	"setmetatable":   "setmetatable(table, metatable) sets the metatable",
	"tonumber":       "tonumber(e [, base]) converts to a number",
	"tostring":       "tostring(v) converts to a string",
	"type":           "type(v) returns the type name",
	"xpcall":         "xpcall(f, msgh [, args...]) protected call with a message handler",
	"_G":             "library: the global environment",
	"_VERSION":       "library: the interpreter version string",
	"coroutine":      "library: coroutine manipulation",
	"debug":          "library: debug interface",
	"io":             "library: input and output",
	"math":           "library: mathematical functions",
	"os":             "library: operating system facilities",
	"package":        "library: module loading",
	"string":         "library: string manipulation",
	"table":          "library: table manipulation",
	"utf8":           "library: UTF-8 support",
}
