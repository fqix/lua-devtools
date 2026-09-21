package lsp

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fqix/lua-devtools/internal/analysis"
)

// Bounds for one workspace scan, so references and rename stay responsive in
// large trees; files beyond them are silently left out.
const (
	scanMaxFiles = 5000
	scanMaxBytes = 64 << 20
	scanMaxFile  = 1 << 20
)

// workspaceFile is one Lua source considered by a cross-file query: an open
// document (with its live text and parse) or a file read from disk.
type workspaceFile struct {
	uri  string
	text []byte
	doc  *document // nil for files that are not open
}

// workspaceFiles lists Lua sources under the workspace roots plus every open
// document, keeping only those whose text contains one of the needles (a cheap
// filter before parsing). Hidden directories and node_modules are skipped, so
// project packages under .lua-devtools/ are reached only through require
// resolution, never renamed. The caller holds s.mu.
func (s *Server) workspaceFiles(needles ...string) []workspaceFile {
	contains := func(text []byte) bool {
		for _, needle := range needles {
			if bytes.Contains(text, []byte(needle)) {
				return true
			}
		}
		return false
	}
	type open struct {
		uri string
		doc *document
	}
	byPath := map[string]open{}
	for uri, doc := range s.docs {
		if path := fileURIPath(uri); path != "" {
			byPath[path] = open{uri: uri, doc: doc}
		}
	}
	var out []workspaceFile
	seen := map[string]bool{}
	add := func(path string, doc open) {
		if seen[path] {
			return
		}
		seen[path] = true
		if doc.doc != nil {
			if contains(doc.doc.text) {
				out = append(out, workspaceFile{uri: doc.uri, text: doc.doc.text, doc: doc.doc})
			}
			return
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > scanMaxFile {
			return
		}
		text, err := os.ReadFile(path)
		if err != nil {
			return
		}
		if contains(text) {
			out = append(out, workspaceFile{uri: pathFileURI(path), text: text})
		}
	}
	files, total := 0, int64(0)
	for _, root := range s.workspaceRoots {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := entry.Name()
			if entry.IsDir() {
				if path != root && (strings.HasPrefix(name, ".") || name == "node_modules") {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(name, ".lua") {
				return nil
			}
			if info, err := entry.Info(); err == nil {
				total += info.Size()
			}
			files++
			if files > scanMaxFiles || total > scanMaxBytes {
				return fs.SkipAll
			}
			add(path, byPath[path])
			return nil
		})
	}
	for path, doc := range byPath {
		add(path, doc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].uri < out[j].uri })
	return out
}

// parse returns the file's analysis with module resolution enabled, and a
// release function. Open documents reuse their live parse.
func (s *Server) parse(file workspaceFile) (*analysis.File, func()) {
	f := file.doc.parsed()
	if f == nil {
		f = analysis.Parse(file.text)
		f.ModuleMembers = s.moduleResolver(file.uri, true)
		return f, f.Close
	}
	f.ModuleMembers = s.moduleResolver(file.uri, true)
	return f, func() { f.ModuleMembers = nil; f.ResetReferences() }
}

func (d *document) parsed() *analysis.File {
	if d == nil {
		return nil
	}
	return d.file
}
