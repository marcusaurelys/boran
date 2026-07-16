package main

import (
	"fmt"
	"io"
	"strings"
)

// RunTAC executes a flat instruction list directly -- no heap, no
// environments, just two maps (vars, temps) and a program counter. It
// exists purely to demonstrate that the optimized TAC produces the same
// observable output as the unoptimized TAC and, ultimately, the same
// output as interpreter.go's tree-walking evaluator for the same source.
func RunTAC(instrs []Instr, out io.Writer) (returnVal RTValue, err error) {
	defer func() {
		if r := recover(); r != nil {
			if re, ok := r.(*RuntimeError); ok {
				err = re
				return
			}
			panic(r)
		}
	}()

	labels := map[string]int{}
	for i, in := range instrs {
		if in.Op == OpLabel {
			labels[in.Label] = i
		}
	}

	vars := map[string]RTValue{}
	temps := map[string]RTValue{}

	resolve := func(op Operand) RTValue {
		switch o := op.(type) {
		case ConstOperand:
			return o.Val
		case VarOperand:
			if v, ok := vars[o.Name]; ok {
				return v
			}
			rtPanic(0, 0, "TAC: undefined variable %q", o.Name)
		case TempOperand:
			if v, ok := temps[o.String()]; ok {
				return v
			}
			rtPanic(0, 0, "TAC: undefined temporary %q", o.String())
		}
		rtPanic(0, 0, "TAC: unresolvable operand")
		return nil
	}

	store := func(dst Operand, v RTValue) {
		switch d := dst.(type) {
		case VarOperand:
			vars[d.Name] = v
		case TempOperand:
			temps[d.String()] = v
		default:
			rtPanic(0, 0, "TAC: invalid assignment target")
		}
	}

	pc := 0
	for pc < len(instrs) {
		in := instrs[pc]
		switch in.Op {
		case OpLabel:
			// no-op marker

		case OpCopy, OpLoadVar, OpStoreVar:
			store(in.Dst, resolve(in.Src1))

		case OpNeg:
			v := resolve(in.Src1)
			switch val := v.(type) {
			case *IntVal:
				store(in.Dst, &IntVal{Val: -val.Val})
			case *FloatVal:
				store(in.Dst, &FloatVal{Val: -val.Val})
			default:
				rtPanic(0, 0, "TAC: unary '-' on non-numeric %s", v.TypeTag())
			}

		case OpNot:
			v := resolve(in.Src1)
			b, ok := v.(*BoolVal)
			if !ok {
				rtPanic(0, 0, "TAC: '!' on non-bool %s", v.TypeTag())
			}
			store(in.Dst, &BoolVal{Val: !b.Val})

		case OpGoto:
			pc = labels[in.Label]
			continue

		case OpIfFalseGoto:
			b, ok := resolve(in.Src1).(*BoolVal)
			if !ok {
				rtPanic(0, 0, "TAC: branch condition is non-bool")
			}
			if !b.Val {
				pc = labels[in.Label]
				continue
			}

		case OpPrint:
			parts := make([]string, len(in.Args))
			for i, a := range in.Args {
				parts[i] = resolve(a).String()
			}
			fmt.Fprintln(out, strings.Join(parts, " "))

		case OpReturn:
			if in.Src1 != nil {
				returnVal = resolve(in.Src1)
			}
			return returnVal, nil

		default:
			if binaryOps[in.Op] {
				store(in.Dst, tacBinary(in.Op, resolve(in.Src1), resolve(in.Src2)))
			} else {
				rtPanic(0, 0, "TAC: unimplemented opcode %s", in.Op)
			}
		}
		pc++
	}
	return returnVal, nil
}

func tacBinary(op OpCode, l, r RTValue) RTValue {
	if op == OpAdd {
		if ls, ok := l.(*StringVal); ok {
			if rs, ok := r.(*StringVal); ok {
				return &StringVal{Val: ls.Val + rs.Val}
			}
		}
	}
	if result, ok := foldBinary(op, l, r); ok {
		return result // reuse the exact same arithmetic as constant folding, for consistency
	}
	rtPanic(0, 0, "TAC: operator %s not applicable to %s and %s", op, l.TypeTag(), r.TypeTag())
	return nil
}
