package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// stepController drives line-by-line execution. It's deliberately just a
// StepHook plus a shared stdin reader -- Run()/execStmt() never know
// whether they're being stepped or run straight through, so the two
// execution modes can't silently diverge in behavior (same rubric concern
// as the design note in interpreter.go).
type stepController struct {
	count  int
	reader *bufio.Reader
	auto   bool // once the user types 'c', stop pausing but keep tracing

	outputBuf *bytes.Buffer // shared with interp.Output via io.MultiWriter
	shown     int           // bytes of outputBuf already printed
}

func newStepController(reader *bufio.Reader, outputBuf *bytes.Buffer) *stepController {
	return &stepController{reader: reader, outputBuf: outputBuf}
}

// FlushOutput prints whatever print()-produced output has accumulated
// since the last flush, labeled so it's unambiguous where a program's own
// output ends and the step controller's UI text begins. Used in the full
// paused display, where it always prints the section header -- even with
// nothing new -- consistent with the Symbol Table / Call Stack / Heap
// sections always showing "(empty)" rather than disappearing, so its
// absence never reads as "broken" versus "genuinely nothing printed yet."
func (sc *stepController) FlushOutput() {
	fmt.Println("--- Program Output ---")
	newText, _ := sc.newOutput()
	if newText == "" {
		fmt.Println("  (none yet)")
		return
	}
	fmt.Print(newText)
	if !strings.HasSuffix(newText, "\n") {
		fmt.Println()
	}
}

// flushOutputIfAny is the quiet counterpart used during auto-continue's
// one-line-per-step trace: printing "(none yet)" on every fast-forwarded
// step would bury the trace in noise, so this only prints when there's
// genuinely new output.
func (sc *stepController) flushOutputIfAny() {
	newText, ok := sc.newOutput()
	if !ok || newText == "" {
		return
	}
	fmt.Print(newText)
	if !strings.HasSuffix(newText, "\n") {
		fmt.Println()
	}
}

// newOutput returns whatever's been written to outputBuf since the last
// flush (marking it consumed), and whether outputBuf exists at all.
func (sc *stepController) newOutput() (string, bool) {
	if sc.outputBuf == nil {
		return "", false
	}
	data := sc.outputBuf.Bytes()
	if len(data) <= sc.shown {
		return "", true
	}
	newData := string(data[sc.shown:])
	sc.shown = len(data)
	return newData, true
}

func (sc *stepController) hook(i *Interpreter, node Node, env *Environment, line, col int) {
	sc.count++
	label := getNodeLabel(node) // reuses visualizer.go's node labeling

	// input() is a natural point a user wants to stop and look around,
	// even mid-continue -- re-arm pausing so 'c' doesn't silently carry
	// straight through a user-supplied value to the end of the program.
	i.BeforeInput = func(prompt string) {
		// The prompt was just written into outputBuf by the interpreter
		// (for the log file's record); echo it live right now since the
		// program is about to block for a keystroke, and mark it shown so
		// the next flush doesn't print it a second time.
		fmt.Print(prompt)
		if sc.outputBuf != nil {
			sc.shown = sc.outputBuf.Len()
		}
	}
	i.AfterInput = func() {
		if sc.auto {
			sc.auto = false
			fmt.Println("\n(paused again after input() -- 'c' doesn't skip past user input)")
		}
	}

	if sc.auto {
		sc.flushOutputIfAny()
		fmt.Printf("[step %d] %d:%d  %s\n", sc.count, line, col, label)
		return
	}

	sc.FlushOutput()
	fmt.Println(strings.Repeat("=", 72))
	fmt.Printf("STEP %d — line %d, col %d — %s\n", sc.count, line, col, label)

	fmt.Println("--- Symbol Table (active scope chain) ---")
	fmt.Print(env.String(i.Heap))

	fmt.Println("--- Call Stack ---")
	fmt.Print(i.Stack.String(i.Heap))

	fmt.Println("--- Heap ---")
	fmt.Print(i.Heap.String())

	fmt.Print("[Enter] next step   c = run to completion   > ")
	raw, _ := sc.reader.ReadString('\n')
	switch strings.TrimSpace(raw) {
	case "c", "continue":
		sc.auto = true
		fmt.Println("(continuing without further pauses; still tracing each step)")
	}
}
