package main

import (
	"fmt"
	"os"
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

	fmt.Printf("Parsed %d top-level statement(s).\n", len(program.Statements))

	if len(parser.Errors) > 0 {
		fmt.Printf("\n--- %d syntax error(s) ---\n", len(parser.Errors))
		for _, e := range parser.Errors {
			fmt.Println(" ", e.Error())
		}
	}

	if len(parser.Symbols.Errors) > 0 {
		fmt.Printf("\n--- %d symbol table error(s) ---\n", len(parser.Symbols.Errors))
		for _, e := range parser.Symbols.Errors {
			fmt.Println(" ", e)
		}
	}

	fmt.Println("\n--- Global scope ---")
	printBlockScopes(parser.Symbols.Global, 0)

	if len(parser.Errors) > 0 {
		os.Exit(1)
	}
}
