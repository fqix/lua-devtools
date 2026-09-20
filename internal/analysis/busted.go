package analysis

import (
	"strconv"
	"strings"
	"unicode/utf8"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// BustedTests discovers literal names in Busted's describe/it DSL. Duplicate
// full names are omitted because a CLI name filter cannot select just one.
func (f *File) BustedTests() []TestCase {
	var tests []TestCase
	var visit func(*tree_sitter.Node, []string)
	visit = func(node *tree_sitter.Node, parents []string) {
		if isLuaFunction(node) {
			return
		}
		if node.Kind() == "function_call" {
			fn, args := node.ChildByFieldName("name"), node.ChildByFieldName("arguments")
			if fn == nil || fn.Kind() != "identifier" || args == nil || args.NamedChildCount() != 2 {
				return
			}
			sym, _ := f.SymbolAt(int(fn.StartByte()))
			if sym != nil {
				return
			}
			kind := fn.Utf8Text(f.Text)
			switch kind {
			case "describe", "context", "insulate", "expose", "it", "spec", "test":
			default:
				return
			}
			if args.NamedChild(0).Kind() != "string" {
				return
			}
			name, ok := luaStringLiteral(args.NamedChild(0).Utf8Text(f.Text))
			body := args.NamedChild(1)
			if !ok || !utf8.ValidString(name) || strings.ContainsRune(name, 0) || body.Kind() != "function_definition" {
				return
			}
			names := append(append([]string(nil), parents...), name)
			switch kind {
			case "it", "spec", "test":
				tests = append(tests, testCase(strings.Join(names, " "), node))
			default:
				if block := body.ChildByFieldName("body"); block != nil {
					visit(block, names)
				}
			}
			return
		}
		// Only unconditional declarations have statically known full names.
		if node.Kind() != "chunk" && node.Kind() != "block" {
			return
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			visit(node.NamedChild(i), parents)
		}
	}
	visit(f.tree.RootNode(), nil)
	counts := map[string]int{}
	for _, test := range tests {
		counts[test.Name]++
	}
	out := tests[:0]
	for _, test := range tests {
		if counts[test.Name] == 1 {
			out = append(out, test)
		}
	}
	return out
}

// luaStringLiteral decodes static descriptions without executing Lua.
func luaStringLiteral(text string) (string, bool) {
	if strings.HasPrefix(text, "[") {
		i := 1
		for i < len(text) && text[i] == '=' {
			i++
		}
		if i >= len(text) || text[i] != '[' {
			return "", false
		}
		close := "]" + text[1:i] + "]"
		if !strings.HasSuffix(text, close) || len(text) < 2*len(close) {
			return "", false
		}
		value := text[i+1 : len(text)-len(close)]
		value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
		return strings.TrimPrefix(value, "\n"), true
	}
	if len(text) < 2 || (text[0] != '\'' && text[0] != '"') || text[len(text)-1] != text[0] {
		return "", false
	}
	text = text[1 : len(text)-1]
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' {
			out.WriteByte(text[i])
			continue
		}
		i++
		if i >= len(text) {
			return "", false
		}
		switch c := text[i]; c {
		case '\\', '\'', '"':
			out.WriteByte(c)
		case 'a':
			out.WriteByte('\a')
		case 'b':
			out.WriteByte('\b')
		case 'f':
			out.WriteByte('\f')
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		case 'v':
			out.WriteByte('\v')
		case '\n':
			out.WriteByte('\n')
		case '\r':
			out.WriteByte('\n')
			if i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
		case 'z':
			for i+1 < len(text) && strings.ContainsRune(" \t\r\n\v\f", rune(text[i+1])) {
				i++
			}
		case 'x':
			if i+2 >= len(text) {
				return "", false
			}
			n, err := strconv.ParseUint(text[i+1:i+3], 16, 8)
			if err != nil {
				return "", false
			}
			out.WriteByte(byte(n))
			i += 2
		case 'u':
			if i+1 >= len(text) || text[i+1] != '{' {
				return "", false
			}
			end := strings.IndexByte(text[i+2:], '}')
			if end < 0 {
				return "", false
			}
			end += i + 2
			n, err := strconv.ParseUint(text[i+2:end], 16, 32)
			if err != nil || !utf8.ValidRune(rune(n)) {
				return "", false
			}
			out.WriteRune(rune(n))
			i = end
		default:
			if c < '0' || c > '9' {
				return "", false
			}
			end := i + 1
			for end < len(text) && end < i+3 && text[end] >= '0' && text[end] <= '9' {
				end++
			}
			n, err := strconv.ParseUint(text[i:end], 10, 8)
			if err != nil {
				return "", false
			}
			out.WriteByte(byte(n))
			i = end - 1
		}
	}
	return out.String(), true
}
