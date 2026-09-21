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

type moduleSearchPath struct {
	Root      string   `json:"root"`
	Templates []string `json:"templates"`
}

type moduleCandidate struct {
	path string
	root string
}

func templateRoot(template string) string {
	prefix, _, _ := strings.Cut(template, "?")
	return filepath.Dir(prefix + "placeholder")
}

// Prefer the owning workspace. For an opened dependency, retain the environment
// whose configured search directory contains it, so nested requires also work.
func (s *Server) moduleEnvironment(path string) *moduleSearchPath {
	var selected *moduleSearchPath
	for i := range s.modulePaths {
		entry := &s.modulePaths[i]
		if entry.Root != "" && withinDirectory(entry.Root, path) {
			if selected == nil || len(entry.Root) > len(selected.Root) {
				selected = entry
			}
		}
	}
	if selected != nil {
		return selected
	}
	for i := range s.modulePaths {
		entry := &s.modulePaths[i]
		if entry.Root == "" {
			return entry
		}
		for _, template := range entry.Templates {
			if !filepath.IsAbs(template) {
				template = filepath.Join(entry.Root, template)
			}
			if strings.Contains(template, "?") && withinDirectory(templateRoot(template), path) {
				return entry
			}
		}
	}
	return nil
}

// moduleResolver searches selected-environment templates, then local source roots.
// It never executes require, package loaders, or project code. The caller holds
// s.mu, allowing unsaved open documents to override disk content consistently.
func (s *Server) moduleResolver(uri string, definitions ...bool) func(string) []analysis.Member {
	path := fileURIPath(uri)
	if path == "" {
		return nil
	}
	root := filepath.Dir(path)
	foundWorkspace := false
	for _, folder := range s.workspaceRoots {
		if withinDirectory(folder, path) && (!foundWorkspace || len(folder) > len(root)) {
			root = folder
			foundWorkspace = true
		}
	}
	environment := s.moduleEnvironment(path)
	roots := []string{root}
	for dir := filepath.Dir(path); dir != root && withinDirectory(root, dir); dir = filepath.Dir(dir) {
		roots = append(roots, dir)
	}
	cache := map[string][]analysis.Member{}
	// Share root resolution across candidates and nested requires in this request.
	// Empty results also cache failed lookups; the next request retries them.
	canonicalRoots := map[string]string{}
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
		candidates := []moduleCandidate{}
		if environment != nil {
			base := environment.Root
			if base == "" {
				base = root
			}
			for _, template := range environment.Templates {
				if strings.Count(template, "?") != 1 || strings.ContainsRune(template, '\x00') {
					continue
				}
				if !filepath.IsAbs(template) {
					template = filepath.Join(base, template)
				}
				candidate := strings.ReplaceAll(template, "?", filepath.Join(parts...))
				candidates = append(candidates, moduleCandidate{path: candidate, root: templateRoot(template)})
			}
		}
		for _, base := range roots {
			stem := filepath.Join(append([]string{base}, parts...)...)
			candidates = append(candidates,
				moduleCandidate{path: stem + ".lua", root: root},
				moduleCandidate{path: filepath.Join(stem, "init.lua"), root: root},
			)
		}
		for _, candidate := range candidates {
			var text []byte
			for uri, doc := range s.docs {
				if fileURIPath(uri) == candidate.path {
					text = doc.text
					break
				}
			}
			if text == nil {
				canonicalRoot, cached := canonicalRoots[candidate.root]
				if !cached {
					canonicalRoot, _ = filepath.EvalSymlinks(candidate.root)
					canonicalRoots[candidate.root] = canonicalRoot
				}
				if canonicalRoot == "" {
					continue
				}
				canonical, err := filepath.EvalSymlinks(candidate.path)
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
				uri := pathFileURI(candidate.path)
				for i := range members {
					for _, location := range []**analysis.Definition{&members[i].Definition, &members[i].Declaration} {
						if def := *location; def != nil && def.URI == "" {
							copy := *def
							copy.URI = uri
							*location = &copy
						}
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
