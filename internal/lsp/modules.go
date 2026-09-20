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

// moduleResolver uses only the workspace's ?.lua and ?/init.lua conventions.
// It never executes require, package loaders, or project code. The caller holds
// s.mu, allowing unsaved open documents to override disk content consistently.
func (s *Server) moduleResolver(uri string) func(string) []analysis.Member {
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
		stem := filepath.Join(append([]string{root}, parts...)...)
		for _, candidate := range []string{stem + ".lua", filepath.Join(stem, "init.lua")} {
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
			members := module.ExportedMembers()
			module.Close()
			cache[name] = members
			return members
		}
		return nil
	}
	return resolve
}
