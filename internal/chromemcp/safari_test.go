package chromemcp

import (
	"bufio"
	"computer-use-server/internal/harness"
	"computer-use-server/internal/store"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// A fake safaridriver --mcp that behaves like the real one: protocol revision
// 2024-11-05, no annotations, and a plain error while remote automation is off.
func TestSafariHelperProcess(t *testing.T) {
	if os.Getenv("READYRIG_SAFARI_TEST_CHILD") != "1" {
		return
	}
	record := func(v map[string]any) {
		f, _ := os.OpenFile(os.Getenv("READYRIG_SAFARI_TEST_OUT"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		_ = json.NewEncoder(f).Encode(v)
		f.Close()
	}
	record(map[string]any{"event": "spawn"})
	tool := func(name string, props map[string]any) map[string]any {
		return map[string]any{"name": name, "description": "fake " + name, "inputSchema": map[string]any{"type": "object", "properties": props}}
	}
	str := map[string]any{"type": "string"}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     any                        `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &req)
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "Safari", "version": "1.0.0"}}
		case "tools/list":
			result = map[string]any{"tools": []any{
				tool("create_tab", map[string]any{"url": str}),
				tool("navigate_to_url", map[string]any{"url": str}),
				tool("get_page_content", map[string]any{"savePath": str}),
				tool("screenshot", map[string]any{"savePath": str}),
				tool("list_tabs", map[string]any{}),
				tool("browser_dialogs", map[string]any{"action": str}),
				tool("close_tab", map[string]any{"handle": str}),
			}}
		case "tools/call":
			var name string
			_ = json.Unmarshal(req.Params["name"], &name)
			record(map[string]any{"event": "call", "name": name, "arguments": req.Params["arguments"]})
			text := "ok " + name
			failed := false
			if _, off := os.Stat(os.Getenv("READYRIG_SAFARI_TEST_OFF")); off == nil {
				text, failed = "Tool error: Could not create a session: You must enable 'Allow remote automation' in the Developer section of Safari Settings.", true
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": failed}
		default:
			continue
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	os.Exit(0)
}

type safariRig struct {
	b    *SafariBridge
	r    *harness.Registry
	out  string // what the fake child recorded
	off  string // while this file exists the fake reports remote automation as off
	root string // the one approved project
}

func newSafariRig(t *testing.T, setup func(*safariRig)) *safariRig {
	t.Helper()
	dir := t.TempDir()
	rig := &safariRig{out: filepath.Join(dir, "child.jsonl"), off: filepath.Join(dir, "automation-off"), root: t.TempDir()}
	t.Setenv("READYRIG_SAFARI_TEST_OUT", rig.out)
	t.Setenv("READYRIG_SAFARI_TEST_OFF", rig.off)
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rig.r = harness.New(s)
	rig.r.RegisterHelp()
	rig.b = NewSafari(rig.r)
	rig.b.interval = 20 * 1000 * 1000
	rig.b.probe = func(context.Context, string) error { return nil }
	rig.b.launch = func(ctx context.Context, _ string, _ []string, _ []string) (*client, error) {
		return startClient(ctx, os.Args[0], []string{"-test.run=^TestSafariHelperProcess$"}, append(os.Environ(), "READYRIG_SAFARI_TEST_CHILD=1"))
	}
	rig.b.SetRoots(func() []string { return []string{rig.root} })
	if setup != nil {
		setup(rig)
	}
	rig.b.Start(SafariOptions{Command: "safaridriver"})
	t.Cleanup(func() { rig.r.SetPaused(true); rig.b.Close(); rig.r.WaitBackground(); s.Close() })
	return rig
}

func (g *safariRig) ready(t *testing.T) {
	t.Helper()
	waitFor(t, func() bool { return g.b.Status().State == "ready" })
}
func (g *safariRig) events(event string) []map[string]any {
	var out []map[string]any
	for _, rec := range childRecords(g.out) {
		if rec["event"] == event {
			out = append(out, rec)
		}
	}
	return out
}
func (g *safariRig) call(tool, arguments string) error {
	_, _, err := g.r.Invoke(context.Background(), tool, harness.Invocation{Session: "agent", Arguments: json.RawMessage(arguments)})
	return err
}
func names(specs []harness.Spec) []string {
	out := []string{}
	for _, s := range specs {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

func TestSafariToolsAreExposedLazily(t *testing.T) {
	g := newSafariRig(t, nil)
	g.ready(t)
	// The upstream browser_ prefix is not repeated, and the whole catalog is callable.
	all := strings.Join(names(g.r.Specs()), " ")
	if all != "batch help safari_close_tab safari_create_tab safari_dialogs safari_get_page_content safari_list_tabs safari_navigate_to_url safari_screenshot use_tool" {
		t.Fatalf("published tools: %s", all)
	}
	// tools/list shows only the everyday ones; the rest wait behind use_tool.
	listed := strings.Join(names(g.r.ListedSpecs()), " ")
	if listed != "batch help safari_create_tab safari_get_page_content safari_navigate_to_url safari_screenshot use_tool" {
		t.Fatalf("listed tools: %s", listed)
	}
	bySpec := map[string]harness.Spec{}
	for _, s := range g.r.Specs() {
		bySpec[s.Name] = s
	}
	for _, name := range []string{"safari_get_page_content", "safari_screenshot", "safari_list_tabs"} {
		if bySpec[name].Mutating || bySpec[name].Annotations["readOnlyHint"] != true {
			t.Fatalf("%s should be read-only: %+v", name, bySpec[name])
		}
	}
	if !bySpec["safari_create_tab"].Mutating || !strings.Contains(bySpec["safari_create_tab"].Description, "start with") {
		t.Fatalf("create_tab: %+v", bySpec["safari_create_tab"])
	}
	// The catalog was read by a process that has already gone; Safari is not connected yet.
	if n := len(g.events("spawn")); n != 1 {
		t.Fatalf("expected one catalog process, got %d", n)
	}
	g.b.mu.Lock()
	connected := g.b.client != nil
	g.b.mu.Unlock()
	if connected {
		t.Fatal("connected to Safari before any tool was called")
	}
	// The first call opens the connection and later calls reuse it.
	if err := g.call("safari_create_tab", `{"url":"https://example.com"}`); err != nil {
		t.Fatal(err)
	}
	if err := g.call("safari_navigate_to_url", `{"url":"https://example.org"}`); err != nil {
		t.Fatal(err)
	}
	if n := len(g.events("spawn")); n != 2 {
		t.Fatalf("expected one live connection after the catalog, got %d processes", n-1)
	}
	// A tool tools/list does not show still runs through use_tool.
	if err := g.call("use_tool", `{"name":"safari_list_tabs","arguments":{}}`); err != nil {
		t.Fatal(err)
	}
	calls := g.events("call")
	if len(calls) != 3 || calls[2]["name"] != "list_tabs" {
		t.Fatalf("calls reached Safari as %v", calls)
	}
}

func TestSafariSavePathStaysInsideApprovedProjects(t *testing.T) {
	g := newSafariRig(t, nil)
	g.ready(t)
	outside, link := t.TempDir(), filepath.Join(g.root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := g.call("safari_screenshot", fmt.Sprintf(`{"savePath":%q}`, filepath.Join(g.root, "shots", "a.png"))); err != nil {
		t.Fatalf("a path inside the project was refused: %v", err)
	}
	if err := g.call("safari_get_page_content", `{}`); err != nil {
		t.Fatalf("a call without savePath was refused: %v", err)
	}
	for name, path := range map[string]string{
		"outside":        filepath.Join(t.TempDir(), "a.png"),
		"relative":       "a.png",
		"dot-dot":        filepath.Join(g.root, "..", "a.png"),
		"through a link": filepath.Join(link, "a.png"),
		"the root":       g.root,
	} {
		err := g.call("safari_screenshot", fmt.Sprintf(`{"savePath":%q}`, path))
		if err == nil || !strings.Contains(err.Error(), "approved project") {
			t.Fatalf("%s: expected a refusal, got %v", name, err)
		}
	}
	if n := len(g.events("call")); n != 2 {
		t.Fatalf("a refused path still reached Safari: %d calls", n)
	}
}

func TestSafariWithoutRootsWritesNothing(t *testing.T) {
	g := newSafariRig(t, func(g *safariRig) { g.b.SetRoots(nil) })
	g.ready(t)
	if err := g.call("safari_screenshot", fmt.Sprintf(`{"savePath":%q}`, filepath.Join(g.root, "a.png"))); err == nil {
		t.Fatal("saved a file although no project is approved")
	}
}

func TestSafariRemoteAutomationHint(t *testing.T) {
	g := newSafariRig(t, func(g *safariRig) { _ = os.WriteFile(g.off, nil, 0o600) })
	g.ready(t)
	err := g.call("safari_create_tab", `{}`)
	var toolErr *harness.ToolError
	if !errors.As(err, &toolErr) || !strings.Contains(toolErr.Message, "Allow remote automation") {
		t.Fatalf("the failure does not say how to fix it: %v", err)
	}
	if s := g.b.Status(); s.State != "permission_required" || s.Tools != 7 {
		t.Fatalf("status: %+v", s)
	}
	_ = os.Remove(g.off)
	if err := g.call("safari_create_tab", `{}`); err != nil {
		t.Fatal(err)
	}
	if s := g.b.Status(); s.State != "ready" {
		t.Fatalf("the warning stayed after Safari answered: %+v", s)
	}
}

func TestSafariFollowsTheBrowserCapabilityAndSharesItWithChrome(t *testing.T) {
	chrome := harness.Tool{Spec: harness.Spec{Name: "chrome_echo", Category: "browser", InputSchema: map[string]any{"type": "object"}}, Run: func(context.Context, harness.Invocation) (harness.Output, error) {
		return harness.Output{}, nil
	}}
	g := newSafariRig(t, func(g *safariRig) { g.r.ReplaceTools("browser", "chrome_", []harness.Tool{chrome}) })
	g.ready(t)
	if !strings.Contains(strings.Join(names(g.r.Specs()), " "), "chrome_echo") {
		t.Fatal("Safari replaced the Chrome tools")
	}
	// Chrome refreshing its tools leaves Safari's alone.
	g.r.ReplaceTools("browser", "chrome_", nil)
	if !strings.Contains(strings.Join(names(g.r.Specs()), " "), "safari_create_tab") {
		t.Fatal("Chrome removed the Safari tools")
	}
	g.r.ReplaceTools("browser", "chrome_", []harness.Tool{chrome})
	// Turning the capability off removes only Safari's tools and closes its connection.
	if err := g.call("safari_create_tab", `{}`); err != nil {
		t.Fatal(err)
	}
	g.r.Enable("safari", false)
	g.b.Refresh()
	waitFor(t, func() bool { return g.b.Status().State == "disabled" })
	g.b.mu.Lock()
	connected := g.b.client != nil
	g.b.mu.Unlock()
	if connected {
		t.Fatal("the Safari connection stayed open")
	}
	// Only Safari's tools go; the Chrome tool, which has no bridge here, stays.
	if got := strings.Join(names(g.r.Specs()), " "); got != "batch chrome_echo help use_tool" {
		t.Fatalf("after disabling: %s", got)
	}
	g.r.Enable("safari", true)
	g.b.Refresh()
	g.ready(t)
	got := strings.Join(names(g.r.Specs()), " ")
	if !strings.Contains(got, "safari_create_tab") || !strings.Contains(got, "chrome_echo") {
		t.Fatalf("after enabling again: %s", got)
	}
}

func TestSafariUnavailableAndDisabled(t *testing.T) {
	g := newSafariRig(t, func(g *safariRig) {
		g.b.probe = func(context.Context, string) error {
			return errors.New("this Safari has no MCP server; it needs Safari 27 or later")
		}
	})
	waitFor(t, func() bool { return g.b.Status().State == "unavailable" })
	if len(g.r.Specs()) != 3 { // only batch, help and use_tool
		t.Fatalf("tools published for an unavailable Safari: %v", names(g.r.Specs()))
	}
	if n := len(g.events("spawn")); n != 0 {
		t.Fatalf("started Safari although it is unavailable: %d", n)
	}

	off := NewSafari(g.r)
	off.Start(SafariOptions{Disabled: true, Command: "safaridriver"})
	if s := off.Status(); s.State != "disabled" {
		t.Fatalf("status: %+v", s)
	}
}

func TestWithinRoots(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	inside := filepath.Join(root, "a", "b.png")
	for path, want := range map[string]bool{
		inside:                                  true,
		filepath.Join(root, "a", "..", "c.png"): true,
		filepath.Join(other, "x.png"):           false,
		root:                                    false,
		filepath.Join(root, "..", "x.png"):      false,
	} {
		if got := withinRoots(path, []string{root}); got != want {
			t.Errorf("withinRoots(%s) = %v, want %v", path, got, want)
		}
	}
	if withinRoots(inside, nil) || withinRoots(inside, []string{"", "relative"}) {
		t.Error("accepted a path without a usable root")
	}
}

// Chrome and Safari have separate switches: neither one turns the other off.
func TestChromeAndSafariSwitchesAreIndependent(t *testing.T) {
	g := newSafariRig(t, nil)
	g.ready(t)
	g.r.Enable("browser", false) // the Chrome switch
	g.b.Refresh()
	time.Sleep(150 * time.Millisecond) // several reconcile ticks
	if s := g.b.Status(); s.State != "ready" {
		t.Fatalf("turning Chrome off disturbed Safari: %+v", s)
	}
	if !strings.Contains(strings.Join(names(g.r.ListedSpecs()), " "), "safari_create_tab") {
		t.Fatal("Safari tools vanished with the Chrome switch")
	}
	if err := g.call("safari_create_tab", `{}`); err != nil {
		t.Fatalf("a Safari call failed with Chrome off: %v", err)
	}
	g.r.Enable("browser", true)
	g.r.Enable("safari", false) // the Safari switch
	if strings.Contains(strings.Join(names(g.r.ListedSpecs()), " "), "safari_") {
		t.Fatal("Safari tools are still listed with the Safari switch off")
	}
	if err := g.call("safari_create_tab", `{}`); err == nil {
		t.Fatal("a Safari call ran with the Safari switch off")
	}
}

// childRecords reads what a helper process wrote, one JSON value per line.
func childRecords(path string) []map[string]any {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]any
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var v map[string]any
		if json.Unmarshal(scanner.Bytes(), &v) == nil {
			out = append(out, v)
		}
	}
	return out
}
