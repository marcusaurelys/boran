package main

import (
	"fmt"
	"strings"
)

// ============================================================================
// Three-address code (TAC)
//
// This is a deliberately separate, additive layer: it lowers a *restricted*
// subset of Boran -- scalar arithmetic, comparisons, logic, and control
// flow (if/for-while/break/continue/return/print) -- into TAC, runs
// classic optimization passes over it, and can execute the optimized form
// to cross-check output against the tree-walking interpreter.
//
// Structs, arrays, pointers, enums, closures, method calls, and the other
// heap-addressed forms are intentionally NOT lowered here. Building a full
// heap-aware IR would essentially duplicate interpreter.go's memory model
// a second time for no real optimization benefit (those forms are exactly
// the ones where constant folding/CSE don't have much to say). Statements
// outside the supported subset are skipped during lowering and reported,
// not silently dropped -- see IRGen.Unsupported.
// ============================================================================

type OpCode string

const (
	OpAdd OpCode = "ADD"
	OpSub OpCode = "SUB"
	OpMul OpCode = "MUL"
	OpDiv OpCode = "DIV"
	OpMod OpCode = "MOD"
	OpLt  OpCode = "LT"
	OpGt  OpCode = "GT"
	OpLe  OpCode = "LE"
	OpGe  OpCode = "GE"
	OpEq  OpCode = "EQ"
	OpNe  OpCode = "NE"
	OpAnd OpCode = "AND"
	OpOr  OpCode = "OR"
	OpNeg OpCode = "NEG" // unary -
	OpNot OpCode = "NOT" // unary !

	OpCopy     OpCode = "COPY"      // Dst = Src1
	OpLoadVar  OpCode = "LOADVAR"   // Dst(temp) = Src1(var)
	OpStoreVar OpCode = "STOREVAR"  // Dst(var)  = Src1

	OpLabel       OpCode = "LABEL"
	OpGoto        OpCode = "GOTO"
	OpIfFalseGoto OpCode = "IFFALSEGOTO" // if Src1 is false, jump to Label

	OpPrint  OpCode = "PRINT"  // Args...
	OpReturn OpCode = "RETURN" // Src1 optional
)

// binaryOps / unaryOps let callers ask "is this a foldable arithmetic op"
// without a long type switch scattered across optimize.go/tacinterp.go.
var binaryOps = map[OpCode]bool{
	OpAdd: true, OpSub: true, OpMul: true, OpDiv: true, OpMod: true,
	OpLt: true, OpGt: true, OpLe: true, OpGe: true, OpEq: true, OpNe: true,
	OpAnd: true, OpOr: true,
}
var unaryOps = map[OpCode]bool{OpNeg: true, OpNot: true}

// Operand is anything an instruction can read or write: a compiler-
// generated temporary, a source-level variable, or a compile-time
// constant (reusing RTValue -- constants are the same thing at IR time as
// they are at runtime, no need for a second literal representation).
type Operand interface {
	String() string
}

type TempOperand struct{ ID int }

func (t TempOperand) String() string { return fmt.Sprintf("t%d", t.ID) }

type VarOperand struct{ Name string }

func (v VarOperand) String() string { return v.Name }

type ConstOperand struct{ Val RTValue }

func (c ConstOperand) String() string { return c.Val.String() }

// Instr is one TAC instruction. Not every field is used by every opcode --
// see String() below for exactly which fields each opcode reads.
type Instr struct {
	Op    OpCode
	Dst   Operand
	Src1  Operand
	Src2  Operand
	Label string    // target/definition name for OpLabel/OpGoto/OpIfFalseGoto
	Args  []Operand // for OpPrint
}

func (in Instr) String() string {
	switch in.Op {
	case OpLabel:
		return in.Label + ":"
	case OpGoto:
		return "    goto " + in.Label
	case OpIfFalseGoto:
		return fmt.Sprintf("    ifFalse %s goto %s", in.Src1, in.Label)
	case OpPrint:
		parts := make([]string, len(in.Args))
		for i, a := range in.Args {
			parts[i] = a.String()
		}
		return "    print " + strings.Join(parts, ", ")
	case OpReturn:
		if in.Src1 == nil {
			return "    return"
		}
		return "    return " + in.Src1.String()
	case OpCopy, OpLoadVar, OpStoreVar:
		return fmt.Sprintf("    %s = %s", in.Dst, in.Src1)
	case OpNeg, OpNot:
		return fmt.Sprintf("    %s = %s %s", in.Dst, in.Op, in.Src1)
	default:
		return fmt.Sprintf("    %s = %s %s %s", in.Dst, in.Src1, in.Op, in.Src2)
	}
}

// ProgramText renders a full instruction list, one per line -- used for
// the "before optimization" / "after optimization" listings.
func ProgramText(instrs []Instr) string {
	var sb strings.Builder
	for _, in := range instrs {
		sb.WriteString(in.String())
		sb.WriteString("\n")
	}
	return sb.String()
}
