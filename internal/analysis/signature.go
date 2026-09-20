package analysis

import (
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"strings"
)

type Signature struct {
	Label      string
	Parameters []string
	Active     int
}

func (f *File) SignatureAt(offset int) *Signature {
	if offset < 0 || offset > len(f.Text) || f.InStringOrComment(offset) {
		return nil
	}
	if result := f.signatureAt(offset); result != nil {
		return result
	}
	// Complete an unfinished argument list for the parser, without changing offsets.
	text := append(append([]byte{}, f.Text[:offset]...), []byte("nil)")...)
	partial := Parse(text)
	defer partial.Close()
	partial.ModuleMembers = f.ModuleMembers
	return partial.signatureAt(offset)
}

func (f *File) signatureAt(offset int) *Signature {
	var call *tree_sitter.Node
	var visit func(*tree_sitter.Node)
	visit = func(node *tree_sitter.Node) {
		if int(node.StartByte()) > offset || int(node.EndByte()) < offset {
			return
		}
		if node.Kind() == "function_call" {
			args := node.ChildByFieldName("arguments")
			if args != nil && int(args.StartByte()) < offset && offset <= int(args.EndByte()) {
				call = node
			}
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			visit(node.NamedChild(i))
		}
	}
	visit(f.tree.RootNode())
	if call == nil {
		return nil
	}
	callee, args := call.ChildByFieldName("name"), call.ChildByFieldName("arguments")
	if callee == nil || args == nil {
		return nil
	}
	definition := f.inferMemberValue(callee, f.memberState(int(callee.StartByte())), 0).definition
	if definition == nil {
		return nil
	}
	parameters := append([]string{}, definition.Parameters...)
	methodCall := callee.Kind() == "method_index_expression"
	if definition.Method && !methodCall {
		parameters = append([]string{"self"}, parameters...)
	}
	if !definition.Method && methodCall && len(parameters) > 0 {
		parameters = parameters[1:]
	}
	active := 0
	var countSeparators func(*tree_sitter.Node)
	countSeparators = func(node *tree_sitter.Node) {
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child.Kind() == "," && int(child.EndByte()) <= offset {
				active++
			}
			if child.Kind() == "ERROR" || child.Kind() == "expression_list" {
				countSeparators(child)
			}
		}
	}
	countSeparators(args)
	if active >= len(parameters) && len(parameters) > 0 {
		active = len(parameters) - 1
	}
	return &Signature{Label: callee.Utf8Text(f.Text) + "(" + strings.Join(parameters, ", ") + ")", Parameters: parameters, Active: active}
}
