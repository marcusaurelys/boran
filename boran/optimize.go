package main

import (
	"fmt"
)

// OptimizeReport records what each pass actually changed, for a legible
// before/after report rather than just two opaque instruction dumps.
type OptimizeReport struct {
	Rounds        int
	FoldedCount   int
	PropagatedCount int
	CSECount      int
	DCECount      int
}

// Optimize runs constant propagation -> constant folding -> common
// subexpression elimination -> dead code elimination repeatedly until the
// instruction list stops changing (or a round cap is hit, as a safety
// net against any pass interaction that could in principle loop). The
// passes are ordered this way because they feed each other: propagating
// a known constant into an expression is what makes it foldable, folding
// exposes more dead temporaries for DCE to remove, and so on -- matching
// the plan of "const-prop feeds folding feeds DCE."
func Optimize(instrs []Instr) ([]Instr, OptimizeReport) {
	report := OptimizeReport{}
	const maxRounds = 8

	for round := 0; round < maxRounds; round++ {
		before := ProgramText(instrs)

		var n int
		instrs, n = constantPropagate(instrs)
		report.PropagatedCount += n

		instrs, n = constantFold(instrs)
		report.FoldedCount += n

		instrs, n = eliminateCommonSubexprs(instrs)
		report.CSECount += n

		instrs, n = eliminateDeadCode(instrs)
		report.DCECount += n

		report.Rounds = round + 1
		if ProgramText(instrs) == before {
			break // fixed point: nothing changed this round
		}
	}
	return instrs, report
}

// ---- Constant propagation ------------------------------------------------

// constantPropagate substitutes known-constant variables/temps into later
// instructions that read them. Conservative about control flow: since
// this operates on a flat instruction list rather than a real CFG, every
// OpLabel (a possible jump target reached from elsewhere with different
// values) clears everything known so far, rather than risking propagating
// a value that doesn't actually hold at that point.
func constantPropagate(instrs []Instr) ([]Instr, int) {
	known := map[string]ConstOperand{}
	count := 0
	out := make([]Instr, len(instrs))

	sub := func(op Operand) Operand {
		if op == nil {
			return nil
		}
		key := op.String()
		if c, ok := known[key]; ok {
			count++
			return c
		}
		return op
	}

	for i, in := range instrs {
		switch in.Op {
		case OpLabel:
			known = map[string]ConstOperand{} // control-flow join: forget everything
			out[i] = in
			continue

		case OpStoreVar, OpCopy, OpLoadVar:
			in.Src1 = sub(in.Src1)
			if c, ok := in.Src1.(ConstOperand); ok {
				known[in.Dst.String()] = c
			} else {
				delete(known, in.Dst.String())
			}
			out[i] = in

		case OpNeg, OpNot:
			in.Src1 = sub(in.Src1)
			delete(known, in.Dst.String())
			out[i] = in

		case OpPrint:
			args := make([]Operand, len(in.Args))
			for j, a := range in.Args {
				args[j] = sub(a)
			}
			in.Args = args
			out[i] = in

		case OpReturn, OpIfFalseGoto:
			in.Src1 = sub(in.Src1)
			out[i] = in

		default:
			if binaryOps[in.Op] {
				in.Src1 = sub(in.Src1)
				in.Src2 = sub(in.Src2)
				delete(known, in.Dst.String())
			}
			out[i] = in
		}
	}
	return out, count
}

// ---- Constant folding ------------------------------------------------

// constantFold evaluates any instruction whose operands are now all
// ConstOperand at compile time, replacing it with a plain copy of the
// computed result.
func constantFold(instrs []Instr) ([]Instr, int) {
	count := 0
	out := make([]Instr, 0, len(instrs))

	for _, in := range instrs {
		if unaryOps[in.Op] {
			if c, ok := in.Src1.(ConstOperand); ok {
				if result, ok := foldUnary(in.Op, c.Val); ok {
					out = append(out, Instr{Op: OpCopy, Dst: in.Dst, Src1: ConstOperand{Val: result}})
					count++
					continue
				}
			}
		}
		if binaryOps[in.Op] {
			c1, ok1 := in.Src1.(ConstOperand)
			c2, ok2 := in.Src2.(ConstOperand)
			if ok1 && ok2 {
				if result, ok := foldBinary(in.Op, c1.Val, c2.Val); ok {
					out = append(out, Instr{Op: OpCopy, Dst: in.Dst, Src1: ConstOperand{Val: result}})
					count++
					continue
				}
			}
		}
		out = append(out, in)
	}
	return out, count
}

func foldUnary(op OpCode, v RTValue) (RTValue, bool) {
	switch op {
	case OpNeg:
		switch val := v.(type) {
		case *IntVal:
			return &IntVal{Val: -val.Val}, true
		case *FloatVal:
			return &FloatVal{Val: -val.Val}, true
		}
	case OpNot:
		if b, ok := v.(*BoolVal); ok {
			return &BoolVal{Val: !b.Val}, true
		}
	}
	return nil, false
}

func foldBinary(op OpCode, l, r RTValue) (RTValue, bool) {
	lf, lIsF, lok := foldToNumeric(l)
	rf, rIsF, rok := foldToNumeric(r)

	switch op {
	case OpAdd, OpSub, OpMul, OpDiv, OpMod:
		if !lok || !rok {
			return nil, false
		}
		useFloat := lIsF || rIsF
		if !useFloat {
			li, ri := l.(*IntVal).Val, r.(*IntVal).Val
			switch op {
			case OpAdd:
				return &IntVal{Val: li + ri}, true
			case OpSub:
				return &IntVal{Val: li - ri}, true
			case OpMul:
				return &IntVal{Val: li * ri}, true
			case OpDiv:
				if ri == 0 {
					return nil, false // don't fold a division by zero away -- let it error at runtime
				}
				return &IntVal{Val: li / ri}, true
			case OpMod:
				if ri == 0 {
					return nil, false
				}
				return &IntVal{Val: li % ri}, true
			}
		}
		switch op {
		case OpAdd:
			return &FloatVal{Val: lf + rf}, true
		case OpSub:
			return &FloatVal{Val: lf - rf}, true
		case OpMul:
			return &FloatVal{Val: lf * rf}, true
		case OpDiv:
			if rf == 0 {
				return nil, false
			}
			return &FloatVal{Val: lf / rf}, true
		}
		return nil, false

	case OpLt, OpGt, OpLe, OpGe:
		if !lok || !rok {
			return nil, false
		}
		switch op {
		case OpLt:
			return &BoolVal{Val: lf < rf}, true
		case OpGt:
			return &BoolVal{Val: lf > rf}, true
		case OpLe:
			return &BoolVal{Val: lf <= rf}, true
		case OpGe:
			return &BoolVal{Val: lf >= rf}, true
		}

	case OpEq, OpNe:
		eq := foldValuesEqual(l, r)
		if op == OpNe {
			eq = !eq
		}
		return &BoolVal{Val: eq}, true

	case OpAnd, OpOr:
		lb, lok := l.(*BoolVal)
		rb, rok := r.(*BoolVal)
		if !lok || !rok {
			return nil, false
		}
		if op == OpAnd {
			return &BoolVal{Val: lb.Val && rb.Val}, true
		}
		return &BoolVal{Val: lb.Val || rb.Val}, true
	}
	return nil, false
}

func foldToNumeric(v RTValue) (f float64, isFloat bool, ok bool) {
	switch val := v.(type) {
	case *IntVal:
		return float64(val.Val), false, true
	case *FloatVal:
		return val.Val, true, true
	}
	return 0, false, false
}

func foldValuesEqual(l, r RTValue) bool {
	if lf, _, lok := foldToNumeric(l); lok {
		if rf, _, rok := foldToNumeric(r); rok {
			return lf == rf
		}
	}
	switch lv := l.(type) {
	case *StringVal:
		if rv, ok := r.(*StringVal); ok {
			return lv.Val == rv.Val
		}
	case *BoolVal:
		if rv, ok := r.(*BoolVal); ok {
			return lv.Val == rv.Val
		}
	case *CharVal:
		if rv, ok := r.(*CharVal); ok {
			return lv.Val == rv.Val
		}
	}
	return false
}

// ---- Common subexpression elimination -----------------------------------

// eliminateCommonSubexprs replaces a recomputation of an already-computed
// expression with a copy of the temp/var that already holds it. Like
// constantPropagate, it clears everything it knows at every OpLabel
// (a possible join point) and additionally whenever a variable used in a
// remembered expression gets redefined, since the remembered result may
// no longer be valid.
func eliminateCommonSubexprs(instrs []Instr) ([]Instr, int) {
	seen := map[string]Operand{} // "OP src1 src2" -> operand already holding that result
	count := 0
	out := make([]Instr, len(instrs))

	invalidate := func(name string) {
		for k := range seen {
			if containsOperandName(k, name) {
				delete(seen, k)
			}
		}
	}

	for i, in := range instrs {
		switch in.Op {
		case OpLabel:
			seen = map[string]Operand{}
			out[i] = in
			continue

		case OpStoreVar, OpCopy, OpLoadVar:
			out[i] = in
			invalidate(in.Dst.String())
			if _, isConst := in.Src1.(ConstOperand); !isConst {
				// a plain copy is itself a trivial "expression" worth remembering
				seen[fmt.Sprintf("COPY %s", in.Src1)] = in.Dst
			}
			continue
		}

		if unaryOps[in.Op] {
			key := fmt.Sprintf("%s %s", in.Op, in.Src1)
			if existing, ok := seen[key]; ok {
				out[i] = Instr{Op: OpCopy, Dst: in.Dst, Src1: existing}
				count++
			} else {
				out[i] = in
				seen[key] = in.Dst
			}
			continue
		}
		if binaryOps[in.Op] {
			key := fmt.Sprintf("%s %s %s", in.Src1, in.Op, in.Src2)
			if existing, ok := seen[key]; ok {
				out[i] = Instr{Op: OpCopy, Dst: in.Dst, Src1: existing}
				count++
			} else {
				out[i] = in
				seen[key] = in.Dst
			}
			continue
		}
		out[i] = in
	}
	return out, count
}

func containsOperandName(key, name string) bool {
	// Cheap substring check for whether a remembered expression key
	// mentions a given variable name -- good enough at this scale
	// (single-function, illustrative IR) without a real def-use graph.
	for i := 0; i+len(name) <= len(key); i++ {
		if key[i:i+len(name)] == name {
			boundaryBefore := i == 0 || key[i-1] == ' '
			boundaryAfter := i+len(name) == len(key) || key[i+len(name)] == ' '
			if boundaryBefore && boundaryAfter {
				return true
			}
		}
	}
	return false
}

// ---- Dead code elimination -----------------------------------------------

// eliminateDeadCode does two independent things:
//  1. drops unreachable code (anything after an unconditional GOTO/RETURN,
//     up to the next label)
//  2. drops instructions that compute into a temp nothing ever reads
//     (backward liveness). It never removes writes to named *variables*,
//     PRINT, RETURN, labels, or jumps -- those are always potentially
//     observable, so only pure dead *temporaries* are eliminated. This is
//     the conservative, always-correct subset of DCE.
func eliminateDeadCode(instrs []Instr) ([]Instr, int) {
	reachable := markUnreachable(instrs)
	count := 0

	live := map[string]bool{}
	keep := make([]bool, len(instrs))

	for i := len(instrs) - 1; i >= 0; i-- {
		in := instrs[i]
		if !reachable[i] {
			count++
			continue
		}
		hasSideEffect := in.Op == OpPrint || in.Op == OpReturn || in.Op == OpLabel ||
			in.Op == OpGoto || in.Op == OpIfFalseGoto || in.Op == OpStoreVar

		definesTemp := (in.Dst != nil)
		if !hasSideEffect && definesTemp {
			if _, isTemp := in.Dst.(TempOperand); isTemp {
				if !live[in.Dst.String()] {
					count++
					continue // dead temp -- drop, don't mark its operands live
				}
			}
		}

		keep[i] = true
		if in.Dst != nil {
			delete(live, in.Dst.String())
		}
		markLive(live, in.Src1)
		markLive(live, in.Src2)
		for _, a := range in.Args {
			markLive(live, a)
		}
	}

	out := make([]Instr, 0, len(instrs))
	for i, in := range instrs {
		if reachable[i] && keep[i] {
			out = append(out, in)
		}
	}
	return out, count
}

func markLive(live map[string]bool, op Operand) {
	if op == nil {
		return
	}
	if _, isConst := op.(ConstOperand); isConst {
		return
	}
	live[op.String()] = true
}

// markUnreachable flags instructions that follow an unconditional GOTO or
// RETURN with no intervening label to re-enter at.
func markUnreachable(instrs []Instr) []bool {
	reachable := make([]bool, len(instrs))
	live := true
	for i, in := range instrs {
		if in.Op == OpLabel {
			live = true // a label is always a potential entry point
		}
		reachable[i] = live
		if live && (in.Op == OpGoto || in.Op == OpReturn) {
			live = false
		}
	}
	return reachable
}
