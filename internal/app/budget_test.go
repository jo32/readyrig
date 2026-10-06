package app

// Budget tests guard the size of what every session pays for before it does
// any work: the tool definitions. They were measured and cut in 0.6.13 (see
// changelog/0.6.13.md and scripts/bench-tools.py). A change that makes them larger
// should be deliberate: raise the budget in the same commit and say why in
// changelog/unreleased.md.

import (
	"computer-use-server/internal/harness"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

const (
	// toolListBudget bounds the tools/list payload of the native tools (the
	// Chrome tools come from the upstream server and are measured separately).
	toolListBudget = 18500
	// toolBudget bounds one tool's definition; descriptionBudget its description.
	toolBudget        = 2700
	descriptionBudget = 560
)

// definitionBytes is the size of a tool as tools/list sends it.
func definitionBytes(s harness.Spec) int {
	annotations := s.Annotations
	if annotations == nil {
		annotations = map[string]any{"readOnlyHint": !s.Mutating, "destructiveHint": s.Mutating, "openWorldHint": s.Category != "files"}
	}
	b, _ := json.Marshal(map[string]any{"name": s.Name, "description": s.Description, "inputSchema": s.InputSchema, "annotations": annotations})
	return len(b)
}

// toolBudgetErrors lists every way a set of tool definitions exceeds its budgets.
func toolBudgetErrors(specs []harness.Spec) []string {
	var errs []string
	total := 0
	for _, s := range specs {
		n := definitionBytes(s)
		total += n
		if n > toolBudget {
			errs = append(errs, fmt.Sprintf("%s: definition is %d bytes, budget %d", s.Name, n, toolBudget))
		}
		if len(s.Description) > descriptionBudget {
			errs = append(errs, fmt.Sprintf("%s: description is %d characters, budget %d", s.Name, len(s.Description), descriptionBudget))
		}
	}
	if total > toolListBudget {
		errs = append(errs, fmt.Sprintf("tools/list is %d bytes for %d native tools, budget %d", total, len(specs), toolListBudget))
	}
	sort.Strings(errs)
	return errs
}

func TestToolDefinitionsStayWithinBudget(t *testing.T) {
	a, err := New(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	reg := a.Server.Registry
	// Count every capability: the budget must hold for the largest real configuration.
	reg.Enable("terminal", true)
	reg.Enable("computer", true)
	specs := reg.ListedSpecs()
	total := 0
	for _, s := range specs {
		total += definitionBytes(s)
		t.Logf("%-20s %5d bytes, description %3d", s.Name, definitionBytes(s), len(s.Description))
	}
	t.Logf("%d tools, %d bytes (budget %d)", len(specs), total, toolListBudget)
	for _, e := range toolBudgetErrors(specs) {
		t.Error(e)
	}
	for _, want := range []string{"batch", "read_file", "edit_file", "exec_command", "list_tasks", "computer_action"} {
		found := false
		for _, s := range specs {
			found = found || s.Name == want
		}
		if !found {
			t.Errorf("%s is not advertised: a budget met by dropping a tool is not a saving", want)
		}
	}
}
func TestToolBudgetCheckerCatchesRegressions(t *testing.T) {
	// Negative controls: the guard must fail when a definition or the list grows.
	bloated := harness.Spec{Name: "bloated", Category: "files", Description: strings.Repeat("x ", 400), InputSchema: harness.Schema(map[string]any{})}
	errs := toolBudgetErrors([]harness.Spec{bloated})
	if len(errs) != 1 || !strings.Contains(errs[0], "description is 800 characters") {
		t.Fatalf("%v", errs)
	}
	var many []harness.Spec
	for i := 0; i < 40; i++ {
		many = append(many, harness.Spec{Name: fmt.Sprint("tool", i), Category: "files", Description: strings.Repeat("y", 300), InputSchema: harness.Schema(map[string]any{})})
	}
	if errs := toolBudgetErrors(many); len(errs) != 1 || !strings.Contains(errs[0], "tools/list is") {
		t.Fatalf("a long list of individually small tools must still trip the total: %v", errs)
	}
	if errs := toolBudgetErrors([]harness.Spec{{Name: "small", Category: "files", Description: "ok", InputSchema: harness.Schema(map[string]any{})}}); len(errs) != 0 {
		t.Fatalf("a small definition must pass: %v", errs)
	}
}
