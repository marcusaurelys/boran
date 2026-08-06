package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"
)

// irResult carries everything main.go needs out of a TAC lowering +
// optimization + execution pass: the human-readable report text (destined
// for the *_ir.log file), timing split into "prep" (lower+optimize, cheap)
// vs "exec" (running the optimized TAC, what --vs actually compares
// against the tree-walker), and enough summary numbers to build the --vs
// comparison without the caller needing to know about Instr/OptimizeReport
// internals.
type irResult struct {
	Report      string
	OptErr      error // error from running the *optimized* TAC (the "real" one)
	PrepElapsed time.Duration
	ExecElapsed time.Duration
	Unsupported int
	InstrBefore int
	InstrAfter  int
}

// runIR lowers prog's arithmetic/control-flow subset to TAC, optimizes it,
// and runs both the unoptimized and optimized forms (the former purely for
// an internal parity check, the latter as the "real" result). If mirror is
// non-nil, the optimized run's output is *also* streamed there live as it
// executes -- this is how --ir (without --vs) makes TAC the actual
// execution channel instead of just a report. If hasTree is true,
// treeOutput is compared against the optimized TAC's output as an extra
// correctness cross-check against the tree-walking interpreter.
func runIR(prog *Program, mirror io.Writer, treeOutput string, hasTree bool) irResult {
	var res irResult

	// Boran programs are organized as top-level declarations plus a final
	// call (e.g. "main();"), not a flat statement sequence -- so target
	// main()'s body specifically, falling back to the top-level list for
	// script-style files with no functions at all. Function *calls* have
	// no TAC calling convention here (out of scope), so nested helper
	// functions called from main are reported as unsupported.
	stmts := prog.Statements
	note := ""
	if fl, ok := findFunctionBody(prog, "main"); ok {
		stmts = fl.Body.Statements
		note = "(main() function found; note: function calls are unsupported currently so all functions should be inlined into main() :3 )"
	}

	prepStart := time.Now()
	gen := NewIRGen()
	gen.LowerStatements(stmts)
	before := gen.Instrs
	optimized, report := Optimize(before)
	res.PrepElapsed = time.Since(prepStart)
	res.Unsupported = len(gen.Unsupported)
	res.InstrBefore = len(before)
	res.InstrAfter = len(optimized)

	var sb strings.Builder
	fmt.Fprintln(&sb, "--- IR / OPTIMIZATION PIPELINE ---")
	if note != "" {
		fmt.Fprintln(&sb, note)
	}
	if len(gen.Unsupported) > 0 {
		fmt.Fprintf(&sb, "\n(%d statement(s) outside the IR's arithmetic/control-flow subset were NOT lowered -- they do not run at all when TAC is the execution channel)\n", len(gen.Unsupported))
		for _, n := range gen.Unsupported {
			fmt.Fprintln(&sb, "  ", n)
		}
	}

	fmt.Fprintln(&sb, "\n--- TAC (before optimization) ---")
	fmt.Fprint(&sb, ProgramText(before))

	fmt.Fprintln(&sb, "\n--- TAC (after optimization) ---")
	fmt.Fprint(&sb, ProgramText(optimized))

	fmt.Fprintf(&sb, "\n--- OPTIMIZATION REPORT ---\n")
	fmt.Fprintf(&sb, "  fixed point reached after %d round(s)\n", report.Rounds)
	fmt.Fprintf(&sb, "  constants propagated : %d\n", report.PropagatedCount)
	fmt.Fprintf(&sb, "  expressions folded   : %d\n", report.FoldedCount)
	fmt.Fprintf(&sb, "  subexpressions reused (CSE) : %d\n", report.CSECount)
	fmt.Fprintf(&sb, "  dead instructions removed   : %d\n", report.DCECount)
	fmt.Fprintf(&sb, "  instruction count: %d -> %d\n", len(before), len(optimized))

	var beforeOut bytes.Buffer
	_, beforeErr := RunTAC(before, &beforeOut)

	var afterOut bytes.Buffer
	var optWriter io.Writer = &afterOut
	if mirror != nil {
		optWriter = io.MultiWriter(mirror, &afterOut)
	}
	execStart := time.Now()
	_, afterErr := RunTAC(optimized, optWriter)
	res.ExecElapsed = time.Since(execStart)
	res.OptErr = afterErr

	fmt.Fprintln(&sb, "\n--- PARITY CHECK ---")
	if beforeErr != nil {
		fmt.Fprintf(&sb, "  unoptimized TAC run error: %s\n", beforeErr.Error())
	}
	if afterErr != nil {
		fmt.Fprintf(&sb, "  optimized TAC run error: %s\n", afterErr.Error())
	}

	unoptStr := beforeOut.String()
	optStr := afterOut.String()

	fmt.Fprintln(&sb, "  unoptimized TAC output:")
	fmt.Fprint(&sb, indentBlock(unoptStr))
	fmt.Fprintln(&sb, "  optimized TAC output:")
	fmt.Fprint(&sb, indentBlock(optStr))

	if unoptStr == optStr {
		fmt.Fprintln(&sb, "  MATCH: optimized TAC produces identical output to unoptimized TAC.")
	} else {
		fmt.Fprintln(&sb, "  MISMATCH: optimization changed observable output -- this would be a real bug in one of the passes.")
	}

	if hasTree {
		fmt.Fprintln(&sb, "  tree-walking interpreter output (IR-lowerable statements will be a subset of this):")
		fmt.Fprint(&sb, indentBlock(treeOutput))
		if len(gen.Unsupported) == 0 {
			if strings.TrimSpace(optStr) == strings.TrimSpace(treeOutput) {
				fmt.Fprintln(&sb, "  MATCH: TAC output is identical to the tree-walking interpreter's output for this program.")
			} else {
				fmt.Fprintln(&sb, "  MISMATCH: TAC output differs from the tree-walking interpreter -- worth investigating.")
			}
		} else {
			fmt.Fprintln(&sb, "  (skipping the TAC-vs-tree-walker comparison: this program has statements outside the IR subset, so their outputs aren't expected to match exactly.)")
		}
	}

	res.Report = sb.String()
	return res
}

// vsReport renders the --vs end-to-end timing comparison between the
// tree-walking (AST) interpreter and the optimized-TAC execution.
func vsReport(astElapsed time.Duration, ir irResult) string {
	var sb strings.Builder
	fmt.Fprintln(&sb, "--- PERFORMANCE COMPARISON (--vs) ---")
	fmt.Fprintf(&sb, "  AST tree-walking exec time : %s\n", astElapsed)
	fmt.Fprintf(&sb, "  TAC lower+optimize time    : %s\n", ir.PrepElapsed)
	fmt.Fprintf(&sb, "  TAC (optimized) exec time  : %s\n", ir.ExecElapsed)
	fmt.Fprintf(&sb, "  instruction count (after optimization): %d -> %d\n", ir.InstrBefore, ir.InstrAfter)
	if ir.ExecElapsed > 0 && astElapsed > 0 {
		ratio := float64(astElapsed) / float64(ir.ExecElapsed)
		if ratio >= 1 {
			fmt.Fprintf(&sb, "  TAC executes ~%.2fx faster than the AST tree-walker\n", ratio)
		} else {
			fmt.Fprintf(&sb, "  AST tree-walker executes ~%.2fx faster than TAC here (lowering/optimization overhead not paying off on a program this small)\n", 1/ratio)
		}
	}
	if ir.Unsupported > 0 {
		fmt.Fprintf(&sb, "  NOTE: %d statement(s) fell outside the TAC-lowerable subset and only ran in the AST version -- this comparison is not apples-to-apples for this program.\n", ir.Unsupported)
	}
	return sb.String()
}

func findFunctionBody(prog *Program, name string) (*FnLiteral, bool) {
	for _, s := range prog.Statements {
		var declName string
		var val Value
		switch n := s.(type) {
		case *ConstDecl:
			declName, val = n.Name, n.Value
		case *LetDecl:
			declName, val = n.Name, n.Value
		default:
			continue
		}
		if declName == name {
			if fl, ok := val.(*FnLiteral); ok {
				return fl, true
			}
		}
	}
	return nil, false
}

func indentBlock(s string) string {
	if strings.TrimSpace(s) == "" {
		return "    (no output)\n"
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString("    ")
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	return sb.String()
}
