package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func helpResult(t *testing.T, r *Registry, args any) HelpResult {
	t.Helper()
	out, err := invoke(t, r, "help", args)
	if err != nil {
		t.Fatal(err)
	}
	return out.Value.(HelpResult)
}
func TestHelpLiveDefinitionsAndDisabledTools(t *testing.T) {
	r := registryForTest(t)
	r.RegisterHelp()
	r.Register(Tool{Spec: Spec{Name: "terminal_test", Category: "terminal", InputSchema: Schema(map[string]any{"cmd": Prop("string", "command")}, "cmd")}})
	got := helpResult(t, r, map[string]any{})
	if got.Total != 3 || got.Tools[0].Name != "help" || !got.Tools[0].Available || got.Tools[1].Name != "batch" || got.Tools[2].Name != "use_tool" {
		t.Fatal(got)
	}
	got = helpResult(t, r, map[string]any{"name": "terminal_test"})
	if got.Total != 1 || got.Tools[0].Available || got.Tools[0].Enabled || got.Tools[0].UnavailableReason != "capability_disabled" {
		t.Fatal(got)
	}
	if got.Tools[0].InputSchema["required"].([]string)[0] != "cmd" {
		t.Fatal("schema missing")
	}
	if got = helpResult(t, r, map[string]any{"include_disabled": true}); got.Total != 4 {
		t.Fatal(got)
	}
	r.Enable("terminal", true)
	if got = helpResult(t, r, map[string]any{}); got.Total != 4 || !got.Tools[3].Available {
		t.Fatal(got)
	}
	// External schemas use decoded JSON types and may contain arbitrary nested constraints.
	var schema map[string]any
	json.Unmarshal([]byte(`{"type":"object","properties":{"pageId":{"type":"number"}},"required":["pageId"]}`), &schema)
	r.ReplaceCategory("browser", []Tool{{Spec: Spec{Name: "chrome_test", Category: "browser", InputSchema: schema, OutputSchema: map[string]any{"type": "object"}, Annotations: map[string]any{"readOnlyHint": true}}, External: true}})
	got = helpResult(t, r, map[string]any{"name": "chrome_test"})
	if !got.Tools[0].Available || got.Tools[0].OutputSchema == nil || got.Tools[0].Annotations["readOnlyHint"] != true {
		t.Fatal(got)
	}
	r.ReplaceCategory("browser", nil)
	if _, err := invoke(t, r, "help", map[string]any{"name": "chrome_test"}); err == nil {
		t.Fatal("removed Chrome tool still present")
	}
}
func TestHelpWhilePausedAndAudit(t *testing.T) {
	r := registryForTest(t)
	r.RegisterHelp()
	r.Register(Tool{Spec: Spec{Name: "file_test", Category: "files", InputSchema: Schema(map[string]any{})}, Run: func(context.Context, Invocation) (Output, error) {
		t.Fatal("paused operation executed")
		return Output{}, nil
	}})
	r.SetPaused(true)
	got := helpResult(t, r, map[string]any{})
	if !got.Paused || got.Total != 1 || got.Tools[0].Name != "help" {
		t.Fatal(got)
	}
	got = helpResult(t, r, map[string]any{"include_disabled": true})
	if got.Total != 4 || !got.Tools[3].Enabled || got.Tools[3].Available || got.Tools[3].UnavailableReason != "control_paused" {
		t.Fatal(got)
	}
	if _, err := invoke(t, r, "file_test", map[string]any{}); err == nil {
		t.Fatal("pause bypassed")
	}
	if err := r.Enable("system", false); err == nil {
		t.Fatal("help can be disabled")
	}
	_, call, err := r.Invoke(context.Background(), "help", Invocation{Arguments: json.RawMessage(`{"name":"help"}`)})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := r.store.Get(call.ID)
	if err != nil || saved.Status != "success" || saved.Category != "system" {
		t.Fatal(saved, err)
	}
	for _, args := range []any{map[string]any{"name": 123}, map[string]any{"include_disabled": "yes"}, map[string]any{"unknown": true}} {
		if _, err := invoke(t, r, "help", args); err == nil {
			t.Fatal("invalid args accepted", args)
		}
	}
}

func TestHelpCompactAndAdvancedGroup(t *testing.T) {
	r := registryForTest(t)
	r.RegisterHelp()
	called := false
	r.Register(Tool{Spec: Spec{Name: "deep", Category: "files", Group: "advanced", Parallel: true, Description: "Does deep work. Second sentence is long detail.", InputSchema: Schema(map[string]any{"n": Prop("integer", "count")})}, Run: func(context.Context, Invocation) (Output, error) {
		called = true
		return Output{Value: map[string]any{"ok": true}}, nil
	}})
	if specs := r.ListedSpecs(); len(specs) != 3 {
		t.Fatalf("advanced tool advertised: %d", len(specs))
	}
	got := helpResult(t, r, map[string]any{"compact": true})
	if got.Total != 4 || !strings.Contains(got.Note, "use_tool") {
		t.Fatal(got)
	}
	for _, h := range got.Tools {
		if h.InputSchema != nil {
			t.Fatal("compact listing carries schemas")
		}
	}
	if got.Tools[3].Description != "Does deep work." {
		t.Fatal(got.Tools[2].Description)
	}
	b, _ := json.Marshal(got)
	var decoded struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, tool := range decoded.Tools {
		if _, has := tool["inputSchema"]; has {
			t.Fatalf("compact JSON still has inputSchema for %v", tool["name"])
		}
	}
	if full := helpResult(t, r, map[string]any{"name": "deep"}); full.Tools[0].InputSchema == nil {
		t.Fatal("named help lost the schema")
	}
	if _, err := invoke(t, r, "use_tool", map[string]any{"name": "deep", "arguments": map[string]any{"n": 1}, "description": "why"}); err != nil || !called {
		t.Fatalf("use_tool: %v called=%v", err, called)
	}
	if _, err := invoke(t, r, "use_tool", map[string]any{"name": "use_tool"}); err == nil {
		t.Fatal("use_tool recursed")
	}
	r.ExposeAll = true
	if specs := r.ListedSpecs(); len(specs) != 4 {
		t.Fatal("ExposeAll ignored")
	}
}

func TestHelpNamesAndCategory(t *testing.T) {
	r := registryForTest(t)
	r.RegisterHelp()
	r.Register(Tool{Spec: Spec{Name: "alpha", Category: "files", Parallel: true, Description: "Alpha. More detail.", InputSchema: Schema(map[string]any{"n": Prop("integer", "count")})}, Run: func(context.Context, Invocation) (Output, error) { return Output{}, nil }})
	r.Register(Tool{Spec: Spec{Name: "beta", Category: "files", Parallel: true, Description: "Beta.", InputSchema: Schema(map[string]any{})}, Run: func(context.Context, Invocation) (Output, error) { return Output{}, nil }})

	got := helpResult(t, r, map[string]any{"names": []string{"alpha", "missing", "beta"}})
	if got.Total != 2 || got.Tools[0].Name != "alpha" || got.Tools[0].InputSchema == nil || got.Tools[1].Name != "beta" {
		t.Fatal(got)
	}
	if len(got.Unknown) != 1 || got.Unknown[0] != "missing" {
		t.Fatal(got.Unknown)
	}
	// Naming tools wins over compact: the caller asked for the detail.
	if c := helpResult(t, r, map[string]any{"names": []string{"alpha"}, "compact": true}); c.Tools[0].InputSchema == nil || c.Tools[0].Description != "Alpha. More detail." {
		t.Fatal(c)
	}
	files := helpResult(t, r, map[string]any{"category": "files", "compact": true})
	if files.Total != 2 || files.Tools[0].InputSchema != nil {
		t.Fatal(files)
	}
	if none := helpResult(t, r, map[string]any{"category": "nope"}); none.Total != 0 {
		t.Fatal(none)
	}
	if _, err := invoke(t, r, "help", map[string]any{"names": "alpha"}); err == nil {
		t.Fatal("names must be an array")
	}
}

func TestHelpSlimListing(t *testing.T) {
	r := registryForTest(t)
	r.RegisterHelp()
	r.Register(Tool{Spec: Spec{Name: "reader", Category: "files", Parallel: true, Description: "Reads. More.", Annotations: map[string]any{"readOnlyHint": true}, InputSchema: Schema(map[string]any{})}, Run: func(context.Context, Invocation) (Output, error) { return Output{}, nil }})
	r.Register(Tool{Spec: Spec{Name: "writer", Category: "files", Mutating: true, Description: "Writes.", InputSchema: Schema(map[string]any{})}, Run: func(context.Context, Invocation) (Output, error) { return Output{}, nil }})
	out, err := invoke(t, r, "help", map[string]any{"compact": true, "slim": true, "category": "files"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out.Value)
	var got struct {
		Note  string           `json:"note"`
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Note, "Slim listing") || len(got.Tools) != 2 {
		t.Fatal(got)
	}
	reader, writer := got.Tools[0], got.Tools[1]
	if reader["parallel"] != true || reader["description"] != "Reads." {
		t.Fatal(reader)
	}
	if writer["mutating"] != true {
		t.Fatal(writer)
	}
	for _, tool := range got.Tools {
		for _, key := range []string{"enabled", "available", "annotations", "inputSchema"} {
			if _, has := tool[key]; has {
				t.Fatalf("slim entry still has %s: %v", key, tool)
			}
		}
	}
	if _, has := reader["mutating"]; has {
		t.Fatal("default mutating=false was sent")
	}
	// Without slim the flags stay, and named lookups are never slimmed.
	full := helpResult(t, r, map[string]any{"names": []string{"reader"}, "slim": true})
	fb, _ := json.Marshal(full.Tools[0])
	if !strings.Contains(string(fb), `"enabled":true`) || full.Tools[0].InputSchema == nil {
		t.Fatal(string(fb))
	}
}
