package computer

import (
	"bytes"
	"computer-use-server/internal/harness"
	"computer-use-server/internal/store"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDriver struct {
	mu          sync.Mutex
	inputs      []Action
	failOn      int
	bounds      Bounds
	pixels      [2]int
	captures    int
	changeUntil int
	clip        string
	windows     []Window
	tree        UITree
	opened      string
	// windowCaptures counts CaptureWindow calls.
	windowCaptures int
}

func newFake() *fakeDriver {
	return &fakeDriver{bounds: Bounds{Width: 500, Height: 250}, pixels: [2]int{1000, 500}}
}
func (*fakeDriver) Permissions() Permissions {
	return Permissions{Supported: true, Screen: true, Accessibility: true}
}
func (f *fakeDriver) Displays() []Bounds { return []Bounds{f.bounds} }
func (f *fakeDriver) Capture(_ context.Context, display int) ([]byte, Bounds, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captures++
	im := image.NewRGBA(image.Rect(0, 0, f.pixels[0], f.pixels[1]))
	if f.captures <= f.changeUntil {
		im.Set(f.captures, 0, color.RGBA{255, 0, 0, 255})
	}
	var b bytes.Buffer
	png.Encode(&b, im)
	return b.Bytes(), f.bounds, nil
}
func (f *fakeDriver) CaptureWindow(_ context.Context, id int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.windowCaptures++
	scale := float64(f.pixels[0]) / f.bounds.Width
	for _, w := range f.windows {
		if w.ID == id {
			var b bytes.Buffer
			png.Encode(&b, image.NewRGBA(image.Rect(0, 0, int(w.Width*scale), int(w.Height*scale))))
			return b.Bytes(), nil
		}
	}
	return nil, errors.New("no such window")
}
func (f *fakeDriver) Input(_ context.Context, a Action) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, a)
	if f.failOn > 0 && len(f.inputs) == f.failOn {
		return errors.New("driver failure")
	}
	return nil
}
func (f *fakeDriver) Clipboard(_ context.Context, set *string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	old := f.clip
	if set != nil {
		f.clip = *set
	}
	return old, nil
}
func (f *fakeDriver) Windows(context.Context) ([]Window, error) { return f.windows, nil }
func (f *fakeDriver) UITree(_ context.Context, app string, _, _ int) (UITree, error) {
	if app == "missing" {
		return UITree{}, errors.New("no window found")
	}
	return f.tree, nil
}
func (f *fakeDriver) OpenApp(_ context.Context, name string) error { f.opened = name; return nil }

func newComputer(t testing.TB, d Driver) *Computer {
	return &Computer{driver: d, dir: t.TempDir(), frames: map[string]frame{}, trees: map[string]treeState{}, interval: time.Millisecond}
}
func inv(session string, v any) harness.Invocation {
	b, _ := json.Marshal(v)
	return harness.Invocation{Session: session, Arguments: b}
}
func shot(t *testing.T, c *Computer, session string, args any) map[string]any {
	t.Helper()
	out, err := c.screenshot(context.Background(), inv(session, args))
	if err != nil {
		t.Fatal(err)
	}
	return out.Value.(map[string]any)
}

func TestRetinaCoordinatesAndSession(t *testing.T) {
	d := newFake()
	d.pixels = [2]int{2560, 1600}
	d.bounds = Bounds{Width: 1280, Height: 800}
	c := newComputer(t, d)
	v := shot(t, c, "one", map[string]any{})
	if v["image_size"].([]int)[0] != 1280 || v["displays"] != 1 || v["display"] != 1 {
		t.Fatal(v)
	}
	args := map[string]any{"action": "left_click", "frame_id": v["frame_id"], "coordinate": []int{640, 400}, "capture_after": false}
	if _, err := c.action(context.Background(), inv("one", args)); err != nil {
		t.Fatal(err)
	}
	if got := d.inputs[0].Coordinate; got[0] != 640 || got[1] != 400 {
		t.Fatal("Retina points mapping wrong", got)
	}
	if _, err := c.action(context.Background(), inv("other", args)); err == nil {
		t.Fatal("cross-session frame allowed")
	}
	id := v["frame_id"].(string)
	f := c.frames[id]
	f.At = time.Now().Add(-6 * time.Minute)
	c.frames[id] = f
	if _, err := c.action(context.Background(), inv("one", args)); err == nil {
		t.Fatal("stale frame allowed")
	}
}
func TestInvalidActions(t *testing.T) {
	c := newComputer(t, newFake())
	for _, args := range []string{`{"action":"left_click","coordinate":[-1,20]}`, `{"action":"left_click","coordinate":[1,2]}`, `{"action":"scroll","scroll_delta":[1]}`, `{"action":"key","keys":[]}`, `{"action":"type","text":""}`, `{"action":"paste","text":""}`, `{"action":"unknown"}`, `{}`, `{"action":"wait"}`, `{"action":"wait","duration_ms":20000}`, `{"action":"key","keys":["enter"],"modifiers":["hyper"]}`, `{"action":"key","keys":["enter"],"actions":[{"action":"key","keys":["tab"]}]}`, `{"actions":[{"action":"key","keys":["tab"],"capture_after":false}]}`} {
		if _, err := c.action(context.Background(), harness.Invocation{Session: "s", Arguments: []byte(args)}); err == nil {
			t.Fatal("invalid input accepted", args)
		}
	}
}
func TestBatchRunsInOrderWithOneScreenshot(t *testing.T) {
	d := newFake()
	c := newComputer(t, d)
	v := shot(t, c, "s", map[string]any{})
	args := map[string]any{"frame_id": v["frame_id"], "settle_ms": 0, "actions": []any{
		map[string]any{"action": "left_click", "coordinate": []int{500, 250}},
		map[string]any{"action": "type", "text": "hello"},
		map[string]any{"action": "key", "keys": []string{"enter"}},
	}}
	before := d.captures
	out, err := c.action(context.Background(), inv("s", args))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.inputs) != 3 || d.inputs[0].Action != "left_click" || d.inputs[1].Text != "hello" || d.inputs[2].Keys[0] != "enter" {
		t.Fatalf("inputs: %+v", d.inputs)
	}
	if d.inputs[0].Coordinate[0] != 250 || d.inputs[0].Coordinate[1] != 125 {
		t.Fatal("batch used the wrong frame", d.inputs[0].Coordinate)
	}
	if d.captures-before != 1 {
		t.Fatalf("batch took %d screenshots", d.captures-before)
	}
	if out.Value.(map[string]any)["steps"] != 3 || out.Screenshot == "" {
		t.Fatal(out.Value)
	}
}
func TestBatchValidatesEverythingBeforeActing(t *testing.T) {
	d := newFake()
	c := newComputer(t, d)
	args := map[string]any{"actions": []any{map[string]any{"action": "key", "keys": []string{"a"}}, map[string]any{"action": "key", "keys": []string{}}}}
	_, err := c.action(context.Background(), inv("s", args))
	if err == nil || !strings.Contains(err.Error(), "step 2") {
		t.Fatalf("error: %v", err)
	}
	if len(d.inputs) != 0 {
		t.Fatal("step 1 ran although step 2 was invalid")
	}
}
func TestBatchStopsAtDriverFailure(t *testing.T) {
	d := newFake()
	d.failOn = 2
	c := newComputer(t, d)
	args := map[string]any{"actions": []any{map[string]any{"action": "key", "keys": []string{"a"}}, map[string]any{"action": "key", "keys": []string{"b"}}, map[string]any{"action": "key", "keys": []string{"c"}}}}
	out, err := c.action(context.Background(), inv("s", args))
	if err == nil || !strings.Contains(err.Error(), "step 2 (key)") {
		t.Fatalf("error: %v", err)
	}
	if v := out.Value.(map[string]any); v["completed"] != 1 || v["steps"] != 3 || len(d.inputs) != 2 {
		t.Fatalf("%v %d", v, len(d.inputs))
	}
}
func TestZoomedRegionMapsBackToTheScreen(t *testing.T) {
	d := newFake()
	c := newComputer(t, d)
	full := shot(t, c, "s", map[string]any{})
	zoom := shot(t, c, "s", map[string]any{"region": []float64{250, 125, 750, 375}, "frame_id": full["frame_id"]})
	if zoom["zoomed"] != true || zoom["image_size"].([]int)[0] != 500 || zoom["image_size"].([]int)[1] != 250 {
		t.Fatalf("zoom frame: %v", zoom["image_size"])
	}
	// The frame is 1000x500 px for a 500x250 pt display; the region starts at pt (125,62.5)
	// and spans 250x125 pt, shown here at 500x250 px.
	if _, err := c.action(context.Background(), inv("s", map[string]any{"action": "left_click", "frame_id": zoom["frame_id"], "coordinate": []int{250, 125}, "capture_after": false})); err != nil {
		t.Fatal(err)
	}
	if got := d.inputs[0].Coordinate; got[0] != 250 || got[1] != 125 {
		t.Fatalf("zoomed click landed at %v", got)
	}
	for _, region := range [][]float64{{0, 0, 2000, 10}, {10, 10, 5, 20}, {-1, 0, 5, 5}} {
		if _, err := c.screenshot(context.Background(), inv("s", map[string]any{"region": region, "frame_id": full["frame_id"]})); err == nil {
			t.Fatalf("bad region %v accepted", region)
		}
	}
	if _, err := c.screenshot(context.Background(), inv("s", map[string]any{"region": []float64{0, 0, 10, 10}})); err == nil {
		t.Fatal("region without frame_id accepted")
	}
	if _, err := c.screenshot(context.Background(), inv("other", map[string]any{"region": []float64{0, 0, 10, 10}, "frame_id": full["frame_id"]})); err == nil {
		t.Fatal("region on another session's frame accepted")
	}
}
func TestSecondDisplayOffset(t *testing.T) {
	d := newFake()
	d.bounds = Bounds{X: -500, Y: 100, Width: 500, Height: 250}
	c := newComputer(t, d)
	v := shot(t, c, "s", map[string]any{})
	if _, err := c.action(context.Background(), inv("s", map[string]any{"action": "mouse_move", "frame_id": v["frame_id"], "coordinate": []int{0, 0}, "capture_after": false})); err != nil {
		t.Fatal(err)
	}
	if got := d.inputs[0].Coordinate; got[0] != -500 || got[1] != 100 {
		t.Fatalf("display origin ignored: %v", got)
	}
}
func TestSettleWaitsForAStableScreen(t *testing.T) {
	d := newFake()
	d.changeUntil = 3
	c := newComputer(t, d)
	if _, err := c.action(context.Background(), inv("s", map[string]any{"action": "wait", "duration_ms": 1, "settle_ms": 2000})); err != nil {
		t.Fatal(err)
	}
	if d.captures < 5 {
		t.Fatalf("captured %d times; the screen changed through capture 3", d.captures)
	}
	d2 := newFake()
	c = newComputer(t, d2)
	if _, err := c.action(context.Background(), inv("s", map[string]any{"action": "wait", "duration_ms": 1})); err != nil {
		t.Fatal(err)
	}
	if d2.captures != 2 {
		t.Fatalf("a still screen needs two matching captures, got %d", d2.captures)
	}
	d3 := newFake()
	d3.changeUntil = 1000
	c = newComputer(t, d3)
	start := time.Now()
	if _, err := c.action(context.Background(), inv("s", map[string]any{"action": "wait", "duration_ms": 1, "settle_ms": 30})); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("settle ignored its time limit")
	}
}
func TestModifiersAndScrollTargets(t *testing.T) {
	d := newFake()
	c := newComputer(t, d)
	v := shot(t, c, "s", map[string]any{})
	for _, args := range []map[string]any{
		{"action": "left_click", "modifiers": []string{"Cmd", "shift"}, "coordinate": []int{10, 10}, "frame_id": v["frame_id"]},
		{"action": "triple_click", "coordinate": []int{20, 20}, "frame_id": v["frame_id"]},
		{"action": "scroll", "scroll_delta": []int{0, -300}, "coordinate": []int{100, 100}, "frame_id": v["frame_id"], "modifiers": []string{"alt"}},
		{"action": "scroll", "scroll_delta": []int{0, 50}},
		{"action": "drag", "coordinate": []int{0, 0}, "to": []int{100, 100}, "frame_id": v["frame_id"]},
	} {
		args["capture_after"] = false
		if _, err := c.action(context.Background(), inv("s", args)); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if d.inputs[0].Flags != 1<<20|1<<17 || d.inputs[1].Action != "triple_click" || d.inputs[2].Flags != 1<<19 {
		t.Fatalf("flags: %+v", d.inputs)
	}
	if len(d.inputs[2].Coordinate) != 2 || d.inputs[2].Coordinate[0] != 50 || len(d.inputs[3].Coordinate) != 0 {
		t.Fatalf("scroll targets: %+v %+v", d.inputs[2], d.inputs[3])
	}
	if d.inputs[4].To[0] != 50 || d.inputs[4].To[1] != 50 {
		t.Fatalf("drag target: %+v", d.inputs[4])
	}
}
func TestPasteBorrowsAndRestoresTheClipboard(t *testing.T) {
	d := newFake()
	d.clip = "user's own text"
	c := newComputer(t, d)
	if _, err := c.action(context.Background(), inv("s", map[string]any{"action": "paste", "text": "你好, world", "capture_after": false})); err != nil {
		t.Fatal(err)
	}
	if len(d.inputs) != 1 || d.inputs[0].Action != "key" || strings.Join(d.inputs[0].Keys, "+") != "cmd+v" {
		t.Fatalf("paste keystroke: %+v", d.inputs)
	}
	if d.clip != "user's own text" {
		t.Fatalf("clipboard not restored: %q", d.clip)
	}
}
func TestClipboardTool(t *testing.T) {
	d := newFake()
	c := newComputer(t, d)
	if _, err := c.clipboard(context.Background(), inv("s", map[string]any{"action": "set", "text": "copied"})); err != nil || d.clip != "copied" {
		t.Fatal(err, d.clip)
	}
	out, err := c.clipboard(context.Background(), inv("s", map[string]any{"action": "get"}))
	if err != nil || out.Text != "copied" || out.Value.(map[string]any)["length"] != 6 {
		t.Fatal(out, err)
	}
	for _, bad := range []map[string]any{{"action": "set"}, {"action": "clear"}} {
		if _, err := c.clipboard(context.Background(), inv("s", bad)); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}
func TestAppWindowsAndOpen(t *testing.T) {
	d := newFake()
	d.windows = []Window{{PID: 1, App: "Safari", Title: "Docs", X: 0, Y: 25, Width: 800, Height: 600, Frontmost: true}, {PID: 2, App: "Notes", Width: 300, Height: 200}}
	c := newComputer(t, d)
	out, err := c.app(context.Background(), inv("s", map[string]any{"action": "windows"}))
	if err != nil || !strings.Contains(out.Text, "Safari — Docs [0,25 800x600] (frontmost)") || !strings.Contains(out.Text, "Notes — (untitled)") {
		t.Fatalf("%q %v", out.Text, err)
	}
	if _, err = c.app(context.Background(), inv("s", map[string]any{"action": "focus", "app": "Notes"})); err != nil || d.opened != "Notes" {
		t.Fatal(err, d.opened)
	}
	if _, err = c.app(context.Background(), inv("s", map[string]any{"action": "open"})); err == nil {
		t.Fatal("open without an app accepted")
	}
}
func TestUITreeRefsDriveClicks(t *testing.T) {
	d := newFake()
	d.tree = UITree{App: "Notes", PID: 7, Nodes: []UINode{{Depth: 0, Role: "AXWindow", Title: "Note", X: 100, Y: 100, Width: 400, Height: 300}, {Depth: 1, Role: "AXButton", Title: "Save", X: 120, Y: 110, Width: 60, Height: 20}, {Depth: 1, Role: "AXTextArea", Value: "hello", Width: 0, Height: 0}}}
	c := newComputer(t, d)
	out, err := c.uiTree(context.Background(), inv("s", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Text, "e2 AXButton \"Save\" [120,110 60x20]") || !strings.Contains(out.Text, "  e2 ") || !strings.Contains(out.Text, "value=\"hello\"") {
		t.Fatalf("outline:\n%s", out.Text)
	}
	click := map[string]any{"action": "left_click", "element": "e2", "capture_after": false}
	if _, err = c.action(context.Background(), inv("s", click)); err != nil {
		t.Fatal(err)
	}
	if got := d.inputs[0].Coordinate; got[0] != 150 || got[1] != 120 {
		t.Fatalf("element center: %v", got)
	}
	for name, args := range map[string]map[string]any{"unknown": {"action": "left_click", "element": "e99"}, "no position": {"action": "left_click", "element": "e3"}} {
		args["capture_after"] = false
		if _, err = c.action(context.Background(), inv("s", args)); err == nil {
			t.Fatalf("%s element accepted", name)
		}
	}
	if _, err = c.action(context.Background(), inv("other", click)); err == nil {
		t.Fatal("another session used this session's element refs")
	}
	st := c.trees["s"]
	st.at = time.Now().Add(-10 * time.Minute)
	c.trees["s"] = st
	if _, err = c.action(context.Background(), inv("s", click)); err == nil {
		t.Fatal("stale element refs accepted")
	}
	if _, err = c.uiTree(context.Background(), inv("s", map[string]any{"app": "missing"})); err == nil {
		t.Fatal("driver error swallowed")
	}
	for _, bad := range []map[string]any{{"max_nodes": 5000}, {"depth": 99}} {
		if _, err = c.uiTree(context.Background(), inv("s", bad)); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}
func TestActionSchemaDeclaresActionNames(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := harness.New(st)
	newComputer(t, newFake()).Register(r)
	for _, s := range r.Specs() {
		if s.Name != "computer_action" {
			continue
		}
		props := s.InputSchema["properties"].(map[string]any)
		if _, ok := props["action"].(map[string]any)["enum"].([]string); !ok {
			t.Fatal("action has no enum")
		}
		if _, ok := props["actions"]; !ok {
			t.Fatal("no batch property")
		}
		if _, ok := props["description"]; !ok {
			t.Fatal("no description property on a mutating tool")
		}
		if len(s.InputSchema["required"].([]string)) != 0 {
			t.Fatal("action must be optional now that actions[] exists")
		}
		return
	}
	t.Fatal("computer_action not registered")
}
func TestAppScreenshotCapturesTheWindow(t *testing.T) {
	d := newFake() // 500x250 pt display, 2 px per pt
	d.windows = []Window{
		{ID: 1, App: "Google Chrome", Title: "Docs", X: 0, Y: 0, Width: 100, Height: 100, Frontmost: true},
		{ID: 2, App: "Simulator", X: 0, Y: 0, Width: 8, Height: 8},
		{ID: 3, App: "Simulator", Title: "iPhone 17", X: 50, Y: 50, Width: 200, Height: 100},
		{ID: 4, App: "Simulator", Title: "Apple TV", X: 300, Y: 50, Width: 150, Height: 100},
	}
	c := newComputer(t, d)
	click := func(frameID any, p []int) []float64 {
		t.Helper()
		if _, err := c.action(context.Background(), inv("s", map[string]any{"action": "left_click", "frame_id": frameID, "coordinate": p, "capture_after": false})); err != nil {
			t.Fatal(err)
		}
		return d.inputs[len(d.inputs)-1].Coordinate
	}
	v := shot(t, c, "s", map[string]any{"app": "simulator"})
	if size := v["image_size"].([]int); size[0] != 400 || size[1] != 200 {
		t.Fatalf("window frame is %v px, want 400x200", size)
	}
	if d.windowCaptures != 1 || d.captures != 0 {
		t.Fatalf("captured the display %d times and the window %d times", d.captures, d.windowCaptures)
	}
	if win := v["window"].(map[string]any); win["title"] != "iPhone 17" {
		t.Fatalf("picked %v, want the frontmost real window", win)
	}
	if cov, _ := v["covered_by"].([]string); len(cov) != 1 || cov[0] != "Google Chrome" {
		t.Fatalf("covered_by %v", v["covered_by"])
	}
	if s := v["screen_size"].([]float64); s[0] != 500 || s[1] != 250 {
		t.Fatalf("screen_size %v", s)
	}
	if got := click(v["frame_id"], []int{0, 0}); got[0] != 50 || got[1] != 50 {
		t.Fatalf("window corner landed at %v", got)
	}
	if got := click(v["frame_id"], []int{200, 100}); got[0] != 150 || got[1] != 100 {
		t.Fatalf("window middle landed at %v", got)
	}
	// A region of a window frame comes from the window again, not from the screen.
	zoom := shot(t, c, "s", map[string]any{"region": []float64{200, 100, 400, 200}, "frame_id": v["frame_id"]})
	if size := zoom["image_size"].([]int); size[0] != 200 || size[1] != 100 || d.windowCaptures != 2 || d.captures != 0 {
		t.Fatalf("zoom %v px, window captures %d, display captures %d", size, d.windowCaptures, d.captures)
	}
	if got := click(zoom["frame_id"], []int{0, 0}); got[0] != 150 || got[1] != 100 {
		t.Fatalf("zoomed window click landed at %v", got)
	}
	tv := shot(t, c, "s", map[string]any{"app": "Simulator", "window": "apple tv"})
	if tv["window"].(map[string]any)["title"] != "Apple TV" || tv["covered_by"] != nil {
		t.Fatalf("window by title: %v", tv)
	}
	// A window partly off the display is still captured whole.
	d.windows = []Window{{ID: 5, App: "Wide", X: 400, Y: 200, Width: 300, Height: 300}}
	if size := shot(t, c, "s", map[string]any{"app": "wide"})["image_size"].([]int); size[0] != 600 || size[1] != 600 {
		t.Fatalf("partly hidden window is %v px", size)
	}
	d.windows = append(d.windows, Window{ID: 3, App: "Simulator", Title: "iPhone 17", X: 50, Y: 50, Width: 200, Height: 100})
	for _, args := range []map[string]any{
		{"app": "Missing"},
		{"app": "Simulator", "window": "nope"},
		{"window": "iPhone"},
		{"app": "Simulator", "display": 1},
		{"app": "Simulator", "region": []float64{0, 0, 5, 5}, "frame_id": v["frame_id"]},
	} {
		if _, err := c.screenshot(context.Background(), inv("s", args)); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, w := range []Window{{ID: 6, App: "Offscreen", X: 900, Y: 900, Width: 100, Height: 100}, {App: "Numberless", Width: 100, Height: 100}} {
		d.windows = []Window{w}
		if _, err := c.screenshot(context.Background(), inv("s", map[string]any{"app": w.App})); err == nil {
			t.Fatalf("%s window accepted", w.App)
		}
	}
}
