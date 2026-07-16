package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

// runIRPipeline lowers prog's arithmetic/control-flow subset to TAC,
// shows it before and after optimization, executes both forms through
// the standalone TAC interpreter, and reports whether unoptimized TAC,
// optimized TAC, and the tree-walking interpreter (treeOutput, already
// captured from the normal run in main.go) all agree -- the actual
// "optimization preserves behavior" demonstration.
func runIRPipeline(prog *Program, w io.Writer, treeOutput string) {
	mirror := io.MultiWriter(w, os.Stdout)

	fmt.Fprintln(mirror, "\n--- IR / OPTIMIZATION PIPELINE ---")

	// Boran programs are organized as top-level declarations plus a final
	// call (e.g. "main();"), not a flat statement sequence -- so target
	// main()'s body specifically, falling back to the top-level list for
	// script-style files with no functions at all. Function *calls* have
	// no TAC calling convention here (out of scope -- see ir.go's header
	// comment), so nested helper-function calls inside main would still
	// be reported as unsupported; write the demo logic directly in main.
	stmts := prog.Statements
	if fl, ok := findFunctionBody(prog, "main"); ok {
		stmts = fl.Body.Statements
		fmt.Fprintln(mirror, "(lowering the body of 'main' -- function calls have no TAC calling convention here, so nested helper-function calls are reported as unsupported)")
	}

	gen := NewIRGen()
	gen.LowerStatements(stmts)

	if len(gen.Unsupported) > 0 {
		fmt.Fprintf(mirror, "\n(%d statement(s) outside the IR's arithmetic/control-flow subset were left to the tree-walking interpreter only -- not shown in TAC below)\n", len(gen.Unsupported))
		for _, note := range gen.Unsupported {
			fmt.Fprintln(mirror, "  ", note)
		}
	}

	fmt.Fprintln(mirror, "\n--- TAC (before optimization) ---")
	before := gen.Instrs
	fmt.Fprint(mirror, ProgramText(before))

	optimized, report := Optimize(before)

	fmt.Fprintln(mirror, "\n--- TAC (after optimization) ---")
	fmt.Fprint(mirror, ProgramText(optimized))

	fmt.Fprintf(mirror, "\n--- OPTIMIZATION REPORT ---\n")
	fmt.Fprintf(mirror, "  fixed point reached after %d round(s)\n", report.Rounds)
	fmt.Fprintf(mirror, "  constants propagated : %d\n", report.PropagatedCount)
	fmt.Fprintf(mirror, "  expressions folded   : %d\n", report.FoldedCount)
	fmt.Fprintf(mirror, "  subexpressions reused (CSE) : %d\n", report.CSECount)
	fmt.Fprintf(mirror, "  dead instructions removed   : %d\n", report.DCECount)
	fmt.Fprintf(mirror, "  instruction count: %d -> %d\n", len(before), len(optimized))

	var beforeOut, afterOut bytes.Buffer
	_, beforeErr := RunTAC(before, &beforeOut)
	_, afterErr := RunTAC(optimized, &afterOut)

	fmt.Fprintln(mirror, "\n--- PARITY CHECK ---")
	if beforeErr != nil {
		fmt.Fprintf(mirror, "  unoptimized TAC run error: %s\n", beforeErr.Error())
	}
	if afterErr != nil {
		fmt.Fprintf(mirror, "  optimized TAC run error: %s\n", afterErr.Error())
	}

	unoptStr := beforeOut.String()
	optStr := afterOut.String()

	fmt.Fprintln(mirror, "  unoptimized TAC output:")
	fmt.Fprint(mirror, indentBlock(unoptStr))
	fmt.Fprintln(mirror, "  optimized TAC output:")
	fmt.Fprint(mirror, indentBlock(optStr))
	fmt.Fprintln(mirror, "  tree-walking interpreter output (IR-lowerable statements will be a subset of this):")
	fmt.Fprint(mirror, indentBlock(treeOutput))

	if unoptStr == optStr {
		fmt.Fprintln(mirror, "  MATCH: optimized TAC produces identical output to unoptimized TAC.")
	} else {
		fmt.Fprintln(mirror, "  MISMATCH: optimization changed observable output -- this would be a real bug in one of the passes.")
	}
	if len(gen.Unsupported) == 0 {
		if strings.TrimSpace(optStr) == strings.TrimSpace(treeOutput) {
			fmt.Fprintln(mirror, "  MATCH: TAC output is identical to the tree-walking interpreter's output for this program.")
		} else {
			fmt.Fprintln(mirror, "  MISMATCH: TAC output differs from the tree-walking interpreter -- worth investigating.")
		}
	} else {
		fmt.Fprintln(mirror, "  (skipping the TAC-vs-tree-walker comparison: this program has statements outside the IR subset, so their outputs aren't expected to match exactly.)")
	}
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
