package analysis

import tree_sitter "github.com/tree-sitter/go-tree-sitter"

// unclosedConstruct adds actionable context when tree-sitter recovers an
// unfinished block or delimiter as one large ERROR node. Tokens inside strings
// and comments never participate. Ambiguous/mismatched closing tokens fall back
// to tree-sitter's ordinary diagnostics rather than suggesting a guessed fix.
func (f *File) unclosedConstruct(root *tree_sitter.Node) *Diagnostic {
	type opening struct {
		token, close string
		start        int
	}
	stack := []opening{}
	ambiguous := false
	var visit func(*tree_sitter.Node)
	visit = func(n *tree_sitter.Node) {
		if ambiguous || n.IsMissing() || n.Kind() == "string" || n.Kind() == "comment" {
			return
		}
		if n.ChildCount() != 0 {
			cursor := n.Walk()
			children := n.Children(cursor)
			cursor.Close()
			for i := range children {
				visit(&children[i])
			}
			return
		}
		token := n.Kind()
		close := ""
		switch token {
		case "function", "if", "do":
			close = "end"
		case "repeat":
			close = "until"
		case "(":
			close = ")"
		case "[":
			close = "]"
		case "{":
			close = "}"
		case "end", "until", ")", "]", "}":
			if len(stack) == 0 || stack[len(stack)-1].close != token {
				ambiguous = true
				return
			}
			stack = stack[:len(stack)-1]
		}
		if close != "" {
			stack = append(stack, opening{token: token, close: close, start: int(n.StartByte())})
		}
	}
	visit(root)
	if ambiguous || len(stack) == 0 {
		return nil
	}
	last := stack[len(stack)-1]
	return &Diagnostic{
		Start: len(f.Text), End: len(f.Text), Key: "diagnostic.unclosed", Arg: last.close,
		Construct: last.token, OpeningLine: f.PositionOf(last.start).Line + 1,
	}
}
