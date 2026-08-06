package main

import (
	"fmt"
	"strings"
)

// PrintAST returns a string representation of the AST starting from the given node.
func PrintAST(n Node) string {
	return printNode(n, 0)
}

func printNode(n Node, depth int) string {
	if n == nil {
		return "<nil>"
	}

	indent := strings.Repeat("  ", depth)
	res := ""

	switch node := n.(type) {
	case *Program:
		for _, stmt := range node.Statements {
			res += printNode(stmt, depth)
		}

	case *Block:
		res += indent + "{\n"
		for _, stmt := range node.Statements {
			res += printNode(stmt, depth+1)
		}
		res += indent + "}\n"

	case *ConstDecl:
		res += fmt.Sprintf("%sCONST %s : %s =\n%s", indent, node.Name, node.TypeName, printValue(node.Value, depth+1))

	case *LetDecl:
		res += fmt.Sprintf("%sLET %s : %s =\n%s", indent, node.Name, node.TypeName, printValue(node.Value, depth+1))

	case *AssignStmt:
		res += fmt.Sprintf("%sASSIGN %s =\n%s", indent, node.Target.String(), printValue(node.Value, depth+1))

	case *ExprStmt:
		res += indent + "EXPR_STMT:\n" + printNode(node.Call, depth+1)

	case *IfStmt:
		res += fmt.Sprintf("%sIF (%s)\n%s", indent, printNode(node.Cond, 0), printNode(node.Then, depth))
		if node.ElseIf != nil {
			res += indent + "ELSE " + strings.TrimSpace(printNode(node.ElseIf, depth)) + "\n"
		} else if node.Else != nil {
			res += indent + "ELSE\n" + printNode(node.Else, depth)
		}

	case *ForIterStmt:
		res += fmt.Sprintf("%sFOR %s IN %s\n%s", indent, node.VarName, printNode(node.Iter, 0), printNode(node.Body, depth))

	case *ForWhileStmt:
		res += fmt.Sprintf("%sFOR_WHILE (%s)\n%s", indent, printNode(node.Cond, 0), printNode(node.Body, depth))

	case *ForRepeatStmt:
		res += fmt.Sprintf("%sFOR_REPEAT\n%s%sUNTIL (%s)\n", indent, printNode(node.Body, depth), indent, printNode(node.Cond, 0))

	case *PrintStmt:
		res += indent + "PRINT\n"
		for _, arg := range node.Args {
			res += printNode(arg, depth+1)
		}

	case *ReturnStmt:
		if node.Value == nil {
			res += indent + "RETURN\n"
		} else {
			res += indent + "RETURN\n" + printNode(node.Value, depth+1)
		}

	case *BreakStmt:
		res += indent + "BREAK\n"

	case *ContinueStmt:
		res += indent + "CONTINUE\n"

	// ---- Expressions ----

	case *BinaryExpr:
		res += fmt.Sprintf("%s( %s %s %s )\n", indent, printNode(node.Left, 0), node.Op, printNode(node.Right, 0))

	case *UnaryExpr:
		if node.Postfix {
			res += fmt.Sprintf("%s( %s%s )\n", indent, printNode(node.Operand, 0), node.Op)
		} else {
			res += fmt.Sprintf("%s( %s%s )\n", indent, node.Op, printNode(node.Operand, 0))
		}

	case *Literal:
		res += fmt.Sprintf("%sLITERAL(%s)\n", indent, node.Value)

	case *Identifier:
		res += fmt.Sprintf("%sID(%s)\n", indent, node.Name)

	case *InputExpr:
		res += fmt.Sprintf("%sINPUT(%s)\n", indent, strings.TrimSpace(printNode(node.Prompt, 0)))

	case *RangeExpr:
		res += indent + "RANGE(\n"
		for _, a := range node.Args {
			res += printNode(a, depth+1)
		}
		res += indent + ")\n"

	case *NewExpr:
		res += indent + "NEW(\n"
		res += printNode(node.Arg, depth+1)
		res += indent + ")\n"

	case *FnCall:
		res += fmt.Sprintf("%sCALL %s(\n", indent, node.Callee)
		for _, arg := range node.Args {
			res += printNode(arg, depth+1)
		}
		res += indent + ")\n"

	case *MethodCall:
		recv := strings.TrimSpace(printNode(node.Base, 0))
		res += fmt.Sprintf("%sMETHOD_CALL %s.%s(\n", indent, recv, node.MethodName)
		for _, arg := range node.Args {
			res += printNode(arg, depth+1)
		}
		res += indent + ")\n"

	case *IndexExpr:
		res += fmt.Sprintf("%sINDEX %s[%s]\n", indent, strings.TrimSpace(printNode(node.Base, 0)), printNode(node.Index, 0))

	case *MemberAccess:
		res += fmt.Sprintf("%sMEMBER %s.%s\n", indent, strings.TrimSpace(printNode(node.Base, 0)), node.Field)

	case *ThisExpr:
		res += indent + "THIS\n"

	case *GroupExpr:
		res += printNode(node.Inner, depth)

	case *CastExpr:
		res += fmt.Sprintf("%sCAST %s AS %s\n", indent, strings.TrimSpace(printNode(node.Operand, 0)), describeType(node.Target))

	default:
		res += fmt.Sprintf("%s<unknown node: %T>\n", indent, n)
	}

	return res
}

// printValue handles the Value interface (literals, array lits, fn lits, etc.)
func printValue(v Value, depth int) string {
	indent := strings.Repeat("  ", depth)
	switch val := v.(type) {
	case *ExprValue:
		return printNode(val.Expr, depth)
	case *ArrLiteral:
		s := indent + "[\n"
		for _, el := range val.Elements {
			s += printValue(el, depth+1)
		}
		return s + indent + "]\n"
	case *FnLiteral:
		params := []string{}
		for _, p := range val.Params {
			params = append(params, fmt.Sprintf("%s:%s", p.Name, p.Type.Kind))
		}
		res := fmt.Sprintf("%sFN (%s)\n", indent, strings.Join(params, ", "))
		res += printNode(val.Body, depth)
		return res
	case *StructLiteral:
		return indent + "STRUCT_DEF\n"
	case *StructInstance:
		return indent + "STRUCT_INST\n"
	case *EnumBody:
		return indent + "ENUM_DEF\n"
	default:
		return indent + "<value>\n"
	}
}
