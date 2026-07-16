package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func printBlockScopes(scope *Scope, depth int) {
	indent := ""
	for i := 0; i < depth; i++ {
		indent += "  "
	}
	for name, sym := range scope.Symbols {
		fmt.Printf("%s- %s : %s (%s) @%d:%d\n", indent, name, sym.TypeName, sym.Kind, sym.Line, sym.Col)
	}
	for _, child := range scope.Children {
		fmt.Printf("%sscope {\n", indent)
		printBlockScopes(child, depth+1)
		fmt.Printf("%s}\n", indent)
	}
}

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintln(os.Stderr, "usage: boran <source_file> [--step]")
		os.Exit(1)
	}
	stepMode := false
	if len(os.Args) == 3 {
		if os.Args[2] != "--step" {
			fmt.Fprintln(os.Stderr, "usage: boran <source_file> [--step]")
			os.Exit(1)
		}
		stepMode = true
	}

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not read %q: %v\n", os.Args[1], err)
		os.Exit(1)
	}

	start := time.Now()
	tokens, _ := TokenizeAll(string(data))
	parser := NewParser(tokens)
	program := parser.ParseProgram()
	elapsed := time.Since(start)

	// 1. Open the file in APPEND mode.
	// O_APPEND: adds to the end. O_CREATE: makes it if missing. O_WRONLY: write only.
	outFile, err := os.OpenFile("parse_results.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not open log file: %v\n", err)
		os.Exit(1)
	}
	defer outFile.Close()

	// 2. Add a unique header for this run
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	separator := strings.Repeat("=", 80)
	fmt.Fprintf(outFile, "\n\n%s\n", separator)
	fmt.Fprintf(outFile, " NEW RUN: %s | Elapsed: %s | Source: %s\n", timestamp, elapsed, os.Args[1])
	fmt.Fprintf(outFile, "%s\n", separator)

	// 3. Print the Tree
	fmt.Fprintln(outFile, "\n--- PARSE TREE ---")
	DrawTree(program, outFile)

	// 4. Print the Symbol Table
	fmt.Fprintln(outFile, "\n--- SYMBOL TABLE ---")
	PrintSymbolTable(parser.Symbols.Global, 0, outFile)

	// 5. Print Errors if any
	if len(parser.Errors) > 0 {
		fmt.Fprintf(outFile, "\n--- SYNTAX ERRORS (%d) ---\n", len(parser.Errors))
		for _, e := range parser.Errors {
			fmt.Fprintln(outFile, "  ", e.Error())
		}
	}

	// 6. Static semantic analysis (only meaningful once the source parses
	//    cleanly enough to produce a usable AST -- still run it regardless,
	//    since parseStmtRecover keeps as much of the tree as it can).
	checker := NewTypeChecker()
	semErrors := checker.Check(program)
	if len(semErrors) > 0 {
		fmt.Fprintf(outFile, "\n--- SEMANTIC ERRORS (%d) ---\n", len(semErrors))
		for _, e := range semErrors {
			fmt.Fprintln(outFile, "  ", e.Error())
		}
	}

	// 7. Execute -- only when the program parsed and type-checked clean.
	//    Running a program full of unresolved names/type errors would just
	//    produce a wall of cascading runtime errors on top of diagnostics
	//    already reported above.
	if len(parser.Errors) == 0 && len(semErrors) == 0 {
		fmt.Fprintln(outFile, "\n--- PROGRAM OUTPUT ---")

		// One shared reader over stdin: the program's own input() calls
		// and the step-controller's "press Enter to continue" prompts
		// read from the same stream, so they can't fight over buffered
		// input across two separate bufio.Readers on the same fd.
		stdinReader := bufio.NewReader(os.Stdin)

		var out io.Writer = outFile
		var outputBuf *bytes.Buffer
		if stepMode {
			outputBuf = &bytes.Buffer{}
			out = io.MultiWriter(outFile, outputBuf)
			fmt.Println("\n--- LINE-BY-LINE EXECUTION ---")
		}

		interp := NewInterpreterWithReader(out, stdinReader)
		var sc *stepController
		if stepMode {
			sc = newStepController(stdinReader, outputBuf)
			interp.OnStep = sc.hook
		}

		if err := interp.Run(program); err != nil {
			fmt.Fprintf(outFile, "\n--- RUNTIME ERROR ---\n  %s\n", err.Error())
			if stepMode {
				fmt.Println("\n--- RUNTIME ERROR ---\n ", err.Error())
			}
		}
		if stepMode {
			sc.FlushOutput() // the last statement's output has no following hook() call to flush it
		}
		fmt.Fprintln(outFile, "\n--- FINAL HEAP STATE ---")
		fmt.Fprint(outFile, interp.Heap.String())
		if stepMode {
			fmt.Println("\n--- FINAL HEAP STATE ---")
			fmt.Print(interp.Heap.String())
		}
	} else {
		fmt.Fprintln(outFile, "\n--- PROGRAM OUTPUT ---\n  (skipped: syntax/semantic errors present)")
	}

	fmt.Printf("Analysis appended to 'parse_results.txt' (Run: %s)\n", timestamp)


	for i:=0;i<5;i++{
fmt.Printf("%d"  ,i)
	}


}
