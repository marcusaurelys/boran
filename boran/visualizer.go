package main

import (
	"fmt"
	"io"
	"reflect"
	"strings"
)

// DrawTree starts the recursive ASCII drawing process
func DrawTree(n Node, w io.Writer) {
	if isActuallyNil(n) {
		return
	}
	drawNode(n, "", true, w)
}

func drawNode(n Node, prefix string, isLast bool, w io.Writer) {
	if isActuallyNil(n) {
		return
	}

	marker := "├── "
	if isLast {
		marker = "└── "
	}

	label := getNodeLabel(n)
	fmt.Fprintf(w, "%s%s%s\n", prefix, marker, label)

	newPrefix := prefix
	if isLast {
		newPrefix += "    "
	} else {
		newPrefix += "│   "
	}

	children := getChildren(n)
	for i, child := range children {
		drawNode(child, newPrefix, i == len(children)-1, w)
	}
}

// isActuallyNil handles "Typed Nils" (interfaces that aren't nil but point to nil)
func isActuallyNil(n Node) bool {
	if n == nil {
		return true
	}
	v := reflect.ValueOf(n)
	if v.Kind() == reflect.Ptr && v.IsNil() {
		return true
	}
	return false
}

func getNodeLabel(n Node) string {
	switch node := n.(type) {
	case *Program:
		return "PROGRAM"
	case *Block:
		return "BLOCK"
	case *ConstDecl:
		return fmt.Sprintf("CONST_DECL [%s : %s]", node.Name, node.TypeName)
	case *LetDecl:
		return fmt.Sprintf("LET_DECL [%s : %s]", node.Name, node.TypeName)
	case *AssignStmt:
		return fmt.Sprintf("ASSIGNMENT -> %s", node.Target.String())
	case *IfStmt:
		return "IF_STMT"
	case *ForIterStmt:
		return fmt.Sprintf("FOR_ITER [var: %s]", node.VarName)
	case *ForWhileStmt:
		return "FOR_WHILE"
	case *ForRepeatStmt:
		return "FOR_REPEAT_UNTIL"
	case *PrintStmt:
		return "PRINT_STMT"
	case *ReturnStmt:
		return "RETURN_STMT"
	case *ExprStmt:
		return "EXPR_STMT"
	case *BreakStmt:
		return "BREAK_STMT"
	case *ContinueStmt:
		return "CONTINUE_STMT"
	case *BinaryExpr:
		return fmt.Sprintf("BINARY_OP (%s)", node.Op)
	case *UnaryExpr:
		var post string
		if node.Postfix {
			post = " (postfix)"
		}
		return fmt.Sprintf("UNARY_OP (%s)%s", node.Op, post)
	case *Literal:
		return fmt.Sprintf("LITERAL (%s)", node.Value)
	case *Identifier:
		return fmt.Sprintf("IDENTIFIER (%s)", node.Name)
	case *InputExpr:
		return "INPUT_EXPR"
	case *FnCall:
		return fmt.Sprintf("FN_CALL (%s)", node.Callee)
	case *MethodCall:
		return fmt.Sprintf("METHOD_CALL [.%s]", node.MethodName)
	case *MemberAccess:
		return fmt.Sprintf("MEMBER_ACCESS [.%s]", node.Field)
	case *IndexExpr:
		return "INDEX_ACCESS"
	case *ThisExpr:
		return "THIS"
	case *GroupExpr:
		return "PAREN_EXPR"
	case *ExprValue:
		return "VALUE_EXPR"
	case *ArrLiteral:
		return "ARRAY_LITERAL"
	case *StructLiteral:
		return "STRUCT_DEFINITION"
	case *StructInstance:
		return "STRUCT_INSTANCE"
	case *EnumBody:
		return "ENUM_DEFINITION"
	case *FnLiteral:
		return "FN_LITERAL"
	case structFieldNode:
		kind := "const"
		if node.mutable {
			kind = "let"
		}
		return fmt.Sprintf("FIELD_DEF [%s %s : %s]", kind, node.name, node.typeName)
	case instanceFieldNode:
		return fmt.Sprintf("FIELD_INIT [%s]", node.name)
	default:
		return fmt.Sprintf("NODE (%T)", n)
	}
}

// Wrapper nodes for clearer struct/instance printing
type structFieldNode struct {
	pos
	name, typeName string
	mutable        bool
	val            Node
}
type instanceFieldNode struct {
	pos
	name string
	val  Node
}

func getChildren(n Node) []Node {
	var children []Node

	// Helper to only add non-nil children
	add := func(nodes ...Node) {
		for _, node := range nodes {
			if !isActuallyNil(node) {
				children = append(children, node)
			}
		}
	}

	switch node := n.(type) {
	case *Program:
		for _, s := range node.Statements {
			add(s)
		}
	case *Block:
		for _, s := range node.Statements {
			add(s)
		}
	case *ConstDecl:
		add(node.Value)
	case *LetDecl:
		add(node.Value)
	case *AssignStmt:
		for _, suf := range node.Target.Suffixes {
			if suf.Kind == SuffixIndex {
				add(suf.Index)
			}
		}
		add(node.Value)
	case *IfStmt:
		add(node.Cond, node.Then, node.ElseIf, node.Else)
	case *ForIterStmt:
		add(node.Iter, node.Body)
	case *ForWhileStmt:
		add(node.Cond, node.Body)
	case *ForRepeatStmt:
		add(node.Body, node.Cond)
	case *ExprStmt:
		add(node.Call)
	case *BinaryExpr:
		add(node.Left, node.Right)
	case *UnaryExpr:
		add(node.Operand)
	case *GroupExpr:
		add(node.Inner)
	case *FnCall:
		for _, a := range node.Args {
			add(a)
		}
	case *MethodCall:
		add(node.Base)
		for _, a := range node.Args {
			add(a)
		}
	case *MemberAccess:
		add(node.Base)
	case *IndexExpr:
		add(node.Base, node.Index)
	case *InputExpr:
		add(node.Prompt)
	case *PrintStmt:
		for _, a := range node.Args {
			add(a)
		}
	case *ExprValue:
		add(node.Expr)
	case *ArrLiteral:
		for _, el := range node.Elements {
			add(el)
		}
	case *FnLiteral:
		add(node.Body)
	case *StructLiteral:
		for _, f := range node.Fields {
			add(structFieldNode{name: f.Name, typeName: f.TypeName, mutable: f.Mutable, val: f.Default})
		}
	case *StructInstance:
		for _, f := range node.Fields {
			add(instanceFieldNode{name: f.Name, val: f.Value})
		}
	case structFieldNode:
		add(node.val)
	case instanceFieldNode:
		add(node.val)
	}
	return children
}

func PrintSymbolTable(scope *Scope, depth int, w io.Writer) {
	indent := strings.Repeat("  ", depth)

	if len(scope.Symbols) > 0 {
		fmt.Fprintf(w, "%sScope Level %d:\n", indent, depth)
		fmt.Fprintf(w, "%s  %-15s | %-10s | %-10s | %-24s | %-10s\n",
			indent, "NAME", "KIND", "TYPE", "DETAILS", "LINE:COL")
		fmt.Fprintf(w, "%s  %s\n", indent, strings.Repeat("-", 80))

		for name, sym := range scope.Symbols {
			fmt.Fprintf(w, "%s  %-15s | %-10s | %-10s | %-24s | %d:%d\n",
				indent, name, sym.Kind, sym.TypeName, symbolDetails(sym), sym.Line, sym.Col)
		}
		fmt.Fprintln(w)
	}

	for _, child := range scope.Children {
		PrintSymbolTable(child, depth+1, w)
	}
}

// symbolDetails surfaces info that TypeName's flat tag loses: an array's
// length and element type, or a pointer's pointee type.
func symbolDetails(sym *Symbol) string {
	if sym.Type == nil {
		return ""
	}
	switch sym.Type.Kind {
	case "array":
		return fmt.Sprintf("length=%d, elem=%s", sym.Type.ArrLen, describeType(sym.Type.Elem))
	case "ptr":
		return fmt.Sprintf("points to %s", describeType(sym.Type.Elem))
	default:
		return ""
	}
}

// describeType renders a DatatypeNode recursively, e.g. "int[5]", "float*",
// "int*[3]". Used for DETAILS, so nested array-of-pointer / pointer-to-array
// combinations stay readable instead of just showing the outermost tag.
func describeType(t *DatatypeNode) string {
	if t == nil {
		return "?"
	}
	switch t.Kind {
	case "array":
		return fmt.Sprintf("%s[%d]", describeType(t.Elem), t.ArrLen)
	case "ptr":
		return fmt.Sprintf("%s*", describeType(t.Elem))
	case "named":
		return t.Name
	default:
		return t.Kind
	}
}
