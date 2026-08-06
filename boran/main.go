package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Exit codes. Kept small and stable so the caller (shell script, test
// harness, whatever) can branch on $? without parsing stderr text.
const (
	exitOK          = 0
	exitUsage       = 1 // bad args, file not found, couldn't write a requested log file
	exitSyntaxErr   = 2
	exitSemanticErr = 3
	exitRuntimeErr  = 4
)

type runFlags struct {
	step bool
	ir   bool
	log  bool
	vs   bool
}

func parseArgs(args []string) (path string, flags runFlags, err error) {
	if len(args) < 1 {
		return "", flags, fmt.Errorf("usage: boran <source_file> [--step] [--ir] [--log] [--vs]")
	}
	path = args[0]
	for _, a := range args[1:] {
		switch a {
		case "--step":
			flags.step = true
		case "--ir":
			flags.ir = true
		case "--log":
			flags.log = true
		case "--vs":
			flags.vs = true
		default:
			return "", flags, fmt.Errorf("usage: boran <source_file> [--step] [--ir] [--log] [--vs] (unknown flag %q)", a)
		}
	}
	return path, flags, nil
}

// logFilePath decides where the combined-or-not log file goes: next to the
// source file, overwritten each run. --ir (with or without --log) targets
// <base>_ir.log; --log alone targets <base>.log; when both are set,
// everything -- full dump AND the IR report -- goes into the single
// <base>_ir.log per the agreed "combine into one file" behavior.
func logFilePath(dir, base string, flags runFlags) string {
	if flags.ir {
		return filepath.Join(dir, base+"_ir.log")
	}
	return filepath.Join(dir, base+".log")
}

func writeLog(dir, base string, flags runFlags, buf *bytes.Buffer) {
	path := logFilePath(dir, base, flags)
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write log file %q: %v\n", path, err)
		return
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", path)
}

func main() {
	path, flags, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitUsage)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not read %q: %v\n", path, err)
		os.Exit(exitUsage)
	}

	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	dir := filepath.Dir(path)

	tokens, _ := TokenizeAll(string(data))
	parser := NewParser(tokens)
	program := parser.ParseProgram()

	// logBuf accumulates the file-only report content. It only exists (and
	// only gets written out) when the person actually asked for a file via
	// --log and/or --ir -- the default run touches disk not at all.
	var logBuf *bytes.Buffer
	if flags.log || flags.ir {
		logBuf = &bytes.Buffer{}
		fmt.Fprintln(logBuf, "--- PARSE TREE ---")
		DrawTree(program, logBuf)
		fmt.Fprintln(logBuf, "\n--- SYMBOL TABLE ---")
		PrintSymbolTable(parser.Symbols.Global, 0, logBuf)
	}

	if len(parser.Errors) > 0 {
		for _, e := range parser.Errors {
			fmt.Fprintln(os.Stderr, e.Error())
		}
		if logBuf != nil {
			fmt.Fprintf(logBuf, "\n--- SYNTAX ERRORS (%d) ---\n", len(parser.Errors))
			for _, e := range parser.Errors {
				fmt.Fprintln(logBuf, "  ", e.Error())
			}
			writeLog(dir, base, flags, logBuf)
		}
		os.Exit(exitSyntaxErr)
	}

	// Static semantic analysis -- only meaningful once the source parses
	// cleanly enough to produce a usable AST, but run it regardless since
	// parseStmtRecover keeps as much of the tree as it can.
	checker := NewTypeChecker()
	semErrors := checker.Check(program)
	if len(semErrors) > 0 {
		for _, e := range semErrors {
			fmt.Fprintln(os.Stderr, e.Error())
		}
		if logBuf != nil {
			fmt.Fprintf(logBuf, "\n--- SEMANTIC ERRORS (%d) ---\n", len(semErrors))
			for _, e := range semErrors {
				fmt.Fprintln(logBuf, "  ", e.Error())
			}
			writeLog(dir, base, flags, logBuf)
		}
		os.Exit(exitSemanticErr)
	}

	// Past this point the program parsed and type-checked clean. Decide
	// which interpreter(s) actually run:
	//   default / --log / --step        -> AST tree-walker only
	//   --ir (no --vs)                  -> TAC only, TAC IS the execution
	//   --vs (with or without --ir)     -> both, timed against each other
	execAST := !flags.ir || flags.vs
	execTAC := flags.ir || flags.vs

	var runtimeErr error
	var astElapsed time.Duration
	var astOutput string

	if execAST {
		stdinReader := bufio.NewReader(os.Stdin)
		programOutputBuf := &bytes.Buffer{}
		out := io.MultiWriter(os.Stdout, programOutputBuf)

		interp := NewInterpreterWithReader(out, stdinReader)
		var sc *stepController
		if flags.step {
			sc = newStepController(stdinReader, programOutputBuf)
			interp.OnStep = sc.hook
		}

		start := time.Now()
		runErr := interp.Run(program)
		astElapsed = time.Since(start)

		if flags.step {
			sc.FlushOutput()
		}
		if runErr != nil {
			fmt.Fprintln(os.Stderr, runErr.Error())
			runtimeErr = runErr
		}

		astOutput = programOutputBuf.String()

		if logBuf != nil {
			fmt.Fprintln(logBuf, "\n--- PROGRAM OUTPUT ---")
			logBuf.WriteString(astOutput)
			if runErr != nil {
				fmt.Fprintf(logBuf, "\n--- RUNTIME ERROR ---\n  %s\n", runErr.Error())
			}
			fmt.Fprintln(logBuf, "\n--- FINAL HEAP STATE ---")
			fmt.Fprint(logBuf, interp.Heap.String())
		}
	} else if logBuf != nil {
		fmt.Fprintln(logBuf, "\n--- PROGRAM OUTPUT ---\n  (AST tree-walking execution skipped: running via TAC, per --ir)")
	}

	if execTAC {
		// --ir alone: TAC is the real execution channel, so stream the
		// optimized run's output live to stdout as it happens. --vs (with
		// or without --ir) already got its "real" output from the AST run
		// above, so the TAC run here is silent/timed-only.
		var mirror io.Writer
		if flags.ir && !flags.vs {
			mirror = os.Stdout
		}
		ir := runIR(program, mirror, astOutput, execAST)

		if ir.Unsupported > 0 {
			if flags.ir && !flags.vs {
				fmt.Fprintf(os.Stderr, "note: %d statement(s) fell outside the TAC-lowerable subset and did NOT execute (see %s)\n", ir.Unsupported, logFilePath(dir, base, flags))
			} else {
				fmt.Fprintf(os.Stderr, "note: %d statement(s) fell outside the TAC-lowerable subset; --vs comparison covers the AST-only remainder too\n", ir.Unsupported)
			}
		}

		if logBuf != nil {
			fmt.Fprintln(logBuf, "\n"+ir.Report)
		}

		if flags.ir && !flags.vs {
			// TAC was the actual execution: its error (if any) is *the*
			// runtime error for exit-code purposes.
			if ir.OptErr != nil {
				fmt.Fprintln(os.Stderr, ir.OptErr.Error())
				runtimeErr = ir.OptErr
			}
		}

		if flags.vs {
			report := vsReport(astElapsed, ir)
			fmt.Print("\n" + report)
			if logBuf != nil {
				fmt.Fprintln(logBuf, "\n"+report)
			}
		}
	}

	if logBuf != nil {
		writeLog(dir, base, flags, logBuf)
	}

	if runtimeErr != nil {
		os.Exit(exitRuntimeErr)
	}
	os.Exit(exitOK)
}
