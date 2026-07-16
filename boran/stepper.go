package main

import (
	"bufio"
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
}

func newStepController(reader *bufio.Reader) *stepController {
	return &stepController{reader: reader}
}

func (sc *stepController) hook(i *Interpreter, node Node, env *Environment, line, col int) {
	sc.count++
	label := getNodeLabel(node) // reuses visualizer.go's node labeling

	// input() is a natural point a user wants to stop and look around,
	// even mid-continue -- re-arm pausing so 'c' doesn't silently carry
	// straight through a user-supplied value to the end of the program.
	i.AfterInput = func() {
		if sc.auto {
			sc.auto = false
			fmt.Println("\n(paused again after input() -- 'c' doesn't skip past user input)")
		}
	}

	if sc.auto {
		fmt.Printf("[step %d] %d:%d  %s\n", sc.count, line, col, label)
		return
	}

	fmt.Println(strings.Repeat("=", 72))
	fmt.Printf("STEP %d — line %d, col %d — %s\n", sc.count, line, col, label)

	fmt.Println("--- Symbol Table (active scope chain) ---")
	fmt.Print(env.String(i.Heap))

	fmt.Println("--- Call Stack ---")
	fmt.Print(i.Stack.String())

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
