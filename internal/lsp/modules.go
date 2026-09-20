package lsp

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fqix/lua-devtools/internal/analysis"
)

func fileURIPath(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" || (parsed.Host != "" && parsed.Host != "localhost") {
		return ""
	}
	path := parsed.Path
	if runtime.GOOS == "windows" && len(path) > 2 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return filepath.FromSlash(path)
}

func withinDirectory(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// moduleResolver uses ?.lua and ?/init.lua under the workspace and source ancestors.
// It never executes require, package loaders, or project code. The caller holds
// s.mu, allowing unsaved open documents to override disk content consistently.
func (s *Server) moduleResolver(uri string, definitions ...bool) func(string) []analysis.Member {
	path := fileURIPath(uri)
	if path == "" {
		return nil
	}
	root := filepath.Dir(path)
	for _, folder := range s.workspaceRoots {
		if withinDirectory(folder, path) {
			root = folder
			break
		}
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}
	roots := []string{root}
	for dir := filepath.Dir(path); dir != root && withinDirectory(root, dir); dir = filepath.Dir(dir) {
		roots = append(roots, dir)
	}
	cache := map[string][]analysis.Member{}
	depth := 0
	remaining := 8 << 20
	var resolve func(string) []analysis.Member
	resolve = func(name string) []analysis.Member {
		if cached, ok := cache[name]; ok {
			return cached
		}
		if depth >= 8 || remaining <= 0 {
			return nil
		}
		parts := strings.Split(name, ".")
		for _, part := range parts {
			if !analysis.IsIdentifier(part) {
				return nil
			}
		}
		cache[name] = nil // Mark before recursion, so cycles terminate.
		depth++
		defer func() { depth-- }()
		candidates := []string{}
		for _, base := range roots {
			stem := filepath.Join(append([]string{base}, parts...)...)
			candidates = append(candidates, stem+".lua", filepath.Join(stem, "init.lua"))
		}
		for _, candidate := range candidates {
			var text []byte
			for uri, doc := range s.docs {
				if fileURIPath(uri) == candidate {
					text = doc.text
					break
				}
			}
			if text == nil {
				canonical, err := filepath.EvalSymlinks(candidate)
				if err != nil || !withinDirectory(canonicalRoot, canonical) {
					continue
				}
				file, err := os.Open(canonical)
				if err != nil {
					continue
				}
				info, err := file.Stat()
				if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
					file.Close()
					continue
				}
				text, err = io.ReadAll(io.LimitReader(file, (1<<20)+1))
				file.Close()
				if err != nil || len(text) > 1<<20 {
					continue
				}
			}
			if len(text) > 1<<20 || len(text) > remaining {
				return nil
			}
			remaining -= len(text)
			module := analysis.Parse(text)
			module.ModuleMembers = resolve
			var members []analysis.Member
			if len(definitions) > 0 && definitions[0] {
				members = module.ExportedDefinitions()
				for i := range members {
					if def := members[i].Definition; def != nil && def.URI == "" {
						copy := *def
						copy.URI = pathFileURI(candidate)
						members[i].Definition = &copy
					}
				}
			} else {
				members = module.ExportedMembers()
			}
			module.Close()
			cache[name] = members
			return members
		}
		return nil
	}
	return resolve
}

func pathFileURI(path string) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}
