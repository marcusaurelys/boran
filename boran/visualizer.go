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
		return fmt.Sprintf("ASSIGNMENT -> %s", node.Target.Name)
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
		return fmt.Sprintf("METHOD_CALL [%s.%s]", node.Receiver, node.MethodName)
	case *MemberAccess:
		return fmt.Sprintf("MEMBER_ACCESS [%s.%s]", node.Base, node.Field)
	case *IndexExpr:
		return fmt.Sprintf("INDEX_ACCESS [%s]", node.Array)
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
		return fmt.Sprintf("FIELD_DEF [%s : %s]", node.name, node.typeName)
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
		add(node.Target.IndexExpr, node.Value)
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
		for _, a := range node.Args {
			add(a)
		}
	case *MemberAccess:
		// Bare member access has no children nodes, it's a leaf
	case *IndexExpr:
		add(node.Index)
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
			add(structFieldNode{name: f.Name, typeName: f.TypeName, val: f.FnValue})
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

// PrintSymbolTable prints the symbol table in a clean, aligned format
func PrintSymbolTable(scope *Scope, depth int, w io.Writer) {
	indent := strings.Repeat("  ", depth)

	// Print headers for this scope level if it has symbols
	if len(scope.Symbols) > 0 {
		fmt.Fprintf(w, "%sScope Level %d:\n", indent, depth)
		fmt.Fprintf(w, "%s  %-15s | %-10s | %-10s | %-10s\n", indent, "NAME", "KIND", "TYPE", "LINE:COL")
		fmt.Fprintf(w, "%s  %s\n", indent, strings.Repeat("-", 55))

		for name, sym := range scope.Symbols {
			fmt.Fprintf(w, "%s  %-15s | %-10s | %-10s | %d:%d\n",
				indent, name, sym.Kind, sym.TypeName, sym.Line, sym.Col)
		}
		fmt.Fprintln(w)
	}

	for _, child := range scope.Children {
		PrintSymbolTable(child, depth+1, w)
	}
}
