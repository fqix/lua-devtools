package lsp

import (
	"sort"
	"strings"

	"github.com/fqix/lua-devtools/internal/analysis"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

// memberReceiver recognizes incomplete a.b:pre expressions, including spaces
// around separators. Unsupported receivers still suppress global suggestions.
func memberReceiver(text []byte, offset int) ([]string, bool, bool) {
	i := offset
	identifier := func() string {
		end := i
		for i > 0 {
			c := text[i-1]
			if c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
				break
			}
			i--
		}
		return string(text[i:end])
	}
	space := func() {
		for i > 0 && strings.ContainsRune(" \t\r\n", rune(text[i-1])) {
			i--
		}
	}
	identifier() // partially typed member name
	space()
	if i == 0 || (text[i-1] != '.' && text[i-1] != ':') {
		return nil, false, false
	}
	method := text[i-1] == ':'
	i--
	parts := []string{}
	for {
		space()
		name := identifier()
		if !analysis.IsIdentifier(name) {
			return nil, method, true
		}
		parts = append(parts, name)
		space()
		if i == 0 || text[i-1] != '.' {
			break
		}
		i--
	}
	if i > 0 && (text[i-1] == ':' || text[i-1] == ']') {
		return nil, method, true
	}
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	return parts, method, true
}

func memberCompletions(f *analysis.File, offset int, receiver []string, method bool, runtimes ...*interpreter) protocol.CompletionList {
	items := []protocol.CompletionItem{}
	members, declared := f.KnownMembers(receiver, offset)
	if !declared && len(receiver) == 1 {
		if len(runtimes) > 0 && runtimes[0] != nil {
			members = append(members, runtimes[0].members[receiver[0]]...)
		} else {
			for _, name := range strings.Fields(builtinMembers[receiver[0]]) {
				members = append(members, analysis.Member{Name: name, Function: !builtinConstants[receiver[0]+"."+name]})
			}
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	for _, member := range members {
		if method && !member.Function {
			continue
		}
		kind := protocol.CompletionItemKindField
		if member.Function {
			kind = protocol.CompletionItemKindFunction
		}
		detail := strings.Join(receiver, ".") + "." + member.Name
		items = append(items, protocol.CompletionItem{Label: member.Name, Kind: &kind, Detail: &detail})
	}
	return protocol.CompletionList{Items: items}
}

// Lua 5.4 fallback when no trusted interpreter is available.
var builtinMembers = map[string]string{
	"coroutine": "close create isyieldable resume running status wrap yield",
	"debug":     "debug gethook getinfo getlocal getmetatable getregistry getupvalue getuservalue setcstacklimit sethook setlocal setmetatable setupvalue setuservalue traceback upvalueid upvaluejoin",
	"io":        "close flush input lines open output popen read stderr stdin stdout tmpfile type write",
	"math":      "abs acos asin atan ceil cos deg exp floor fmod huge log max maxinteger min mininteger modf pi rad random randomseed sin sqrt tan tointeger type ult",
	"os":        "clock date difftime execute exit getenv remove rename setlocale time tmpname",
	"package":   "config cpath loaded loadlib path preload searchers searchpath",
	"string":    "byte char dump find format gmatch gsub len lower match pack packsize rep reverse sub unpack upper",
	"table":     "concat insert move pack remove sort unpack",
	"utf8":      "char charpattern codepoint codes len offset",
}

var builtinConstants = map[string]bool{
	"io.stderr": true, "io.stdin": true, "io.stdout": true,
	"math.huge": true, "math.maxinteger": true, "math.mininteger": true, "math.pi": true,
	"package.config": true, "package.cpath": true, "package.loaded": true, "package.path": true,
	"package.preload": true, "package.searchers": true, "utf8.charpattern": true,
}
