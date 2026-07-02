package main

import (
	"fmt"
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
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: boran <source_file>")
		os.Exit(1)
	}

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not read %q: %v\n", os.Args[1], err)
		os.Exit(1)
	}

	tokens, _ := TokenizeAll(string(data))
	parser := NewParser(tokens)
	program := parser.ParseProgram()

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
	fmt.Fprintf(outFile, " NEW RUN: %s | Source: %s\n", timestamp, os.Args[1])
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

	fmt.Printf("Analysis appended to 'parse_results.txt' (Run: %s)\n", timestamp)
}
