package lsp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fqix/lua-devtools/internal/analysis"
)

// nativeModule caches one probe of a C module, keyed by its file identity so
// a reinstalled library is probed again. Failures are cached too: a module
// that cannot load must not be retried on every keystroke.
type nativeModule struct {
	modTime time.Time
	size    int64
	members []analysis.Member
}

// Loading the module runs its code; that is the point of the opt-in setting.
const nativeProbe = `local ok, module = pcall(require, "%s")
if not ok then io.stderr:write(tostring(module)) os.exit(3) end
if type(module) ~= "table" then os.exit(0) end
for key, value in pairs(module) do
  if type(key) == "string" then io.write(key, "\t", type(value), "\n") end
end`

// nativeModuleFile locates the shared library the interpreter would load for
// the module: the dotted path first, then Lua's all-in-one loader form, in
// which `a.b` lives in `a.so` and exports luaopen_a_b.
func nativeModuleFile(name string, base string, templates []string) string {
	parts := strings.Split(name, ".")
	for _, template := range templates {
		if strings.Count(template, "?") != 1 || strings.ContainsRune(template, '\x00') {
			continue
		}
		if !filepath.IsAbs(template) {
			template = filepath.Join(base, template)
		}
		candidates := []string{strings.ReplaceAll(template, "?", filepath.Join(parts...))}
		if len(parts) > 1 {
			candidates = append(candidates, strings.ReplaceAll(template, "?", parts[0]))
		}
		for _, candidate := range candidates {
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate
			}
		}
	}
	return ""
}

// nativeModuleMembers lists the exported table of a C module by loading it in
// the selected interpreter. It returns nil when no shared library exists for
// the name (nothing is executed then), or when loading fails. The caller holds s.mu.
func (s *Server) nativeModuleMembers(name, base string, environment *moduleSearchPath) []analysis.Member {
	if s.runtime == nil {
		return nil
	}
	file := nativeModuleFile(name, base, environment.CTemplates)
	if file == "" {
		return nil
	}
	info, err := os.Stat(file)
	if err != nil {
		return nil
	}
	// One library can serve several modules (`pkg.a` and `pkg.b` in pkg.so), so
	// the module name is part of the key.
	key := file + "\x00" + name
	if cached, ok := s.nativeCache[key]; ok && cached.modTime.Equal(info.ModTime()) && cached.size == info.Size() {
		return cached.members
	}
	absolute := func(templates []string) string {
		out := []string{}
		for _, template := range templates {
			if !filepath.IsAbs(template) {
				template = filepath.Join(base, template)
			}
			out = append(out, template)
		}
		// A trailing ";;" keeps the interpreter's built-in search paths.
		return strings.Join(append(out, ";"), ";")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := interpreterCommand(ctx, s.runtime.path, fmt.Sprintf(nativeProbe, name))
	cmd.Dir = base
	cmd.Env = append(cmd.Env, "LUA_PATH="+absolute(environment.Templates), "LUA_CPATH="+absolute(environment.CTemplates))
	output, err := cmd.Output()
	var members []analysis.Member
	if err == nil {
		members = []analysis.Member{}
		for _, line := range strings.Split(strings.TrimSpace(strings.ReplaceAll(string(output), "\r\n", "\n")), "\n") {
			key, kind, ok := bytes.Cut([]byte(line), []byte{'\t'})
			if !ok || !analysis.IsIdentifier(string(key)) {
				continue
			}
			members = append(members, analysis.Member{Name: string(key), Function: string(kind) == "function"})
		}
		sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	}
	if s.nativeCache == nil {
		s.nativeCache = map[string]nativeModule{}
	}
	s.nativeCache[key] = nativeModule{modTime: info.ModTime(), size: info.Size(), members: members}
	return members
}
