package server

// Budget tests guard what every agent pays on every call: the size of results
// and of the server's instructions. They exist because those costs grew
// unnoticed once and were measured and cut in 0.6.13 (see changelog/0.6.13.md and
// scripts/bench-tools.py). A change that makes any of them larger should be
// deliberate: raise the budget in the same commit and say why in
// changelog/unreleased.md.

import (
	"computer-use-server/internal/harness"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// instructionsBudget bounds the text sent once per session.
const instructionsBudget = 700

// resultBudgets are the largest sizes, in bytes, that an agent may receive for
// these operations (text blocks of the MCP reply, or the REST body).
var resultBudgets = map[string]int{
	"write_file":                   40,
	"read_file 3 lines":            32,
	"edit_file":                    120,
	"list_directory":               16,
	"glob":                         16,
	"search_files":                 16,
	"exec echo":                    28,
	"exec exit 3":                  28,
	"error: missing file":          100,
	"exec 200KB output":            31500,
	"list_tasks after 20 commands": 520,
	"batch of 3 reads":             64,
	"REST exec echo (relay)":       280,
}

// budgetErrors compares measured sizes with their budgets.
func budgetErrors(measured map[string]int, budgets map[string]int) []string {
	var errs []string
	for name, limit := range budgets {
		got, ok := measured[name]
		switch {
		case !ok:
			errs = append(errs, name+": not measured")
		case got > limit:
			errs = append(errs, fmt.Sprintf("%s: %d bytes, budget %d", name, got, limit))
		}
	}
	sort.Strings(errs)
	return errs
}
func textBytes(r toolReply) int {
	n := 0
	for _, c := range r.Content {
		if s, ok := c["text"].(string); ok {
			n += len(s)
		}
	}
	return n
}

func TestInstructionsStayWithinBudget(t *testing.T) {
	if n := len(mcpInstructions); n > instructionsBudget {
		t.Fatalf("server instructions are %d bytes, budget %d; every session pays for them", n, instructionsBudget)
	}
}
func TestResultSizesStayWithinBudget(t *testing.T) {
	s := fixture(t)
	s.Registry.RegisterHelp()
	p := harness.NewProcesses(t.TempDir())
	t.Cleanup(func() { p.Stop(); s.Registry.WaitBackground() })
	p.Register(s.Registry)
	s.Registry.Enable("terminal", true)
	c := newMCPClient(t, s)
	m := map[string]int{}
	m["write_file"] = textBytes(c.call("write_file", map[string]any{"path": "a.txt", "content": "alpha\nbeta\ngamma\n"}))
	m["read_file 3 lines"] = textBytes(c.call("read_file", map[string]any{"path": "a.txt"})) - len("alpha\nbeta\ngamma\n")
	m["edit_file"] = textBytes(c.call("edit_file", map[string]any{"path": "a.txt", "old_string": "beta", "new_string": "BETA"}))
	m["list_directory"] = textBytes(c.call("list_directory", map[string]any{})) - len(fmt.Sprintf("%s %9s  %s  %s\n", "f", "17 B", "2026-10-03 08:00", "a.txt"))
	m["glob"] = textBytes(c.call("glob", map[string]any{"pattern": "*.txt"})) - len("a.txt\n")
	m["search_files"] = textBytes(c.call("search_files", map[string]any{"query": "gamma"})) - len("a.txt:3:gamma\n")
	m["exec echo"] = textBytes(c.call("exec_command", map[string]any{"command": "echo hi"}))
	m["exec exit 3"] = textBytes(c.call("exec_command", map[string]any{"command": "exit 3"}))
	m["error: missing file"] = textBytes(c.call("read_file", map[string]any{"path": "absent.txt"}))
	m["exec 200KB output"] = textBytes(c.call("exec_command", map[string]any{"command": "head -c 200000 /dev/zero | tr '\\0' x", "yield_time_ms": 5000}))
	for i := 0; i < 20; i++ {
		c.call("exec_command", map[string]any{"command": "true"})
	}
	m["list_tasks after 20 commands"] = textBytes(c.call("list_tasks", map[string]any{}))
	batch := c.call("batch", map[string]any{"calls": []any{
		map[string]any{"tool": "read_file", "arguments": map[string]any{"path": "a.txt"}},
		map[string]any{"tool": "read_file", "arguments": map[string]any{"path": "a.txt"}},
		map[string]any{"tool": "read_file", "arguments": map[string]any{"path": "a.txt"}},
	}})
	m["batch of 3 reads"] = textBytes(batch) - 3*len("     1\talpha\n     2\tBETA\n     3\tgamma\n")
	w := request(s.Gateway(), "POST", "/api/v1/tools/exec_command", `{"command":"echo hi"}`, s.AccessPath)
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	delete(body, "call_id") // identifiers have a fixed size and are not the tool's choice
	compact, _ := json.Marshal(body)
	m["REST exec echo (relay)"] = len(compact)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("%-26s %6d bytes (budget %d)", k, m[k], resultBudgets[k])
	}
	if os.Getenv("READYRIG_BUDGET_ONLY_LOG") != "" {
		return
	}
	for _, e := range budgetErrors(m, resultBudgets) {
		t.Error(e)
	}
}
func TestBudgetCheckerCatchesRegressions(t *testing.T) {
	// A negative control: the guard must fail when a result grows.
	grown := map[string]int{"exec echo": 41, "write_file": 100}
	errs := budgetErrors(grown, map[string]int{"exec echo": 40, "write_file": 140, "glob": 40})
	if len(errs) != 2 || !strings.Contains(errs[0], "exec echo: 41 bytes, budget 40") || !strings.Contains(errs[1], "glob: not measured") {
		t.Fatalf("%v", errs)
	}
	if errs := budgetErrors(map[string]int{"exec echo": 40}, map[string]int{"exec echo": 40}); len(errs) != 0 {
		t.Fatalf("a result at its budget must pass: %v", errs)
	}
}
