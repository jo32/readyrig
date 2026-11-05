package computer

import (
	"bytes"
	"computer-use-server/internal/harness"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"golang.org/x/image/draw"
	"hash/fnv"
	"image"
	"image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	frameLifetime = 5 * time.Minute
	maxEdge       = 1280
	maxBatch      = 25
	maxTextBytes  = 10000
	maxClipboard  = 1 << 20
	defaultSettle = 1000
)

type Permissions struct {
	Supported     bool   `json:"supported"`
	Screen        bool   `json:"screen"`
	Accessibility bool   `json:"accessibility"`
	Platform      string `json:"platform"`
}

// Bounds is a display's rectangle in points; X and Y locate it in the global
// coordinate space shared by all displays.
type Bounds struct{ X, Y, Width, Height float64 }

// Action is one input step. Coordinates arrive in screenshot pixels and are
// converted to global display points before reaching the Driver.
type Action struct {
	Action     string    `json:"action,omitempty"`
	Coordinate []float64 `json:"coordinate,omitempty"`
	To         []float64 `json:"to,omitempty"`
	Text       string    `json:"text,omitempty"`
	Keys       []string  `json:"keys,omitempty"`
	Scroll     []int     `json:"scroll_delta,omitempty"`
	Modifiers  []string  `json:"modifiers,omitempty"`
	DurationMS int       `json:"duration_ms,omitempty"`
	Element    string    `json:"element,omitempty"`
	FrameID    string    `json:"frame_id,omitempty"`
	// Flags holds the modifier bits resolved from Modifiers.
	Flags uint64 `json:"-"`
}

// Window is an on-screen window.
type Window struct {
	// ID is the window number CaptureWindow takes.
	ID        int     `json:"-"`
	PID       int     `json:"pid"`
	App       string  `json:"app"`
	Title     string  `json:"title"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Width     float64 `json:"width"`
	Height    float64 `json:"height"`
	Layer     int     `json:"layer"`
	Frontmost bool    `json:"frontmost,omitempty"`
}

// UINode is one accessibility element; positions are global display points.
type UINode struct {
	Ref         string  `json:"ref"`
	Depth       int     `json:"depth"`
	Role        string  `json:"role"`
	Title       string  `json:"title,omitempty"`
	Description string  `json:"description,omitempty"`
	Value       string  `json:"value,omitempty"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
}

// UITree is the accessibility outline of one application.
type UITree struct {
	App       string
	PID       int
	Nodes     []UINode
	Truncated bool
}

type Driver interface {
	Permissions() Permissions
	Displays() []Bounds
	// Capture returns a PNG of display (1-based) at native resolution.
	Capture(ctx context.Context, display int) ([]byte, Bounds, error)
	// CaptureWindow returns a PNG of one window's own pixels (without windows in
	// front of it or its shadow) at native resolution.
	CaptureWindow(ctx context.Context, id int) ([]byte, error)
	Input(context.Context, Action) error
	// Clipboard returns the current text and, when set is non-nil, replaces it.
	Clipboard(ctx context.Context, set *string) (string, error)
	Windows(context.Context) ([]Window, error)
	UITree(ctx context.Context, app string, maxNodes, maxDepth int) (UITree, error)
	OpenApp(ctx context.Context, name string) error
}

// frame remembers what a screenshot covered so pixel coordinates in it can be
// mapped back to the screen, including zoomed regions of a display.
type frame struct {
	Width, Height int
	Origin, Span  [2]float64
	Display       int
	At            time.Time
	Session       string
	// WindowID is set when the frame shows one window; WindowBounds was its
	// rectangle, the space a region of the frame is cut from.
	WindowID     int
	WindowBounds Bounds
}
type treeState struct {
	nodes []UINode
	at    time.Time
}
type Computer struct {
	driver   Driver
	dir      string
	mu       sync.Mutex
	frames   map[string]frame
	trees    map[string]treeState
	interval time.Duration
}

func New(dir string) *Computer {
	return &Computer{driver: nativeDriver{}, dir: dir, frames: map[string]frame{}, trees: map[string]treeState{}}
}
func (c *Computer) Permissions() Permissions { return c.driver.Permissions() }

// RequestPermission triggers the system prompt for "screen" or "accessibility"
// so the app is registered in System Settings. Drivers without prompts ignore it.
func (c *Computer) RequestPermission(kind string) {
	if r, ok := c.driver.(interface{ Request(string) }); ok {
		r.Request(kind)
	}
}

// Missing names the macOS permission a tool needs and does not have yet, or
// returns "" when it can run. Screen Recording is checked when the tool takes
// the screenshot itself; actions only need Accessibility because they can be
// asked not to capture.
func (c *Computer) Missing(tool string) string {
	perms := c.driver.Permissions()
	if !perms.Supported {
		return ""
	}
	switch tool {
	case "computer_screenshot":
		if !perms.Screen {
			return "ReadyRig needs the Screen & System Audio Recording permission: turn on ReadyRig in System Settings > Privacy & Security > Screen & System Audio Recording, then restart ReadyRig."
		}
	case "computer_action", "computer_ui_tree":
		if !perms.Accessibility {
			return "ReadyRig needs the Accessibility permission: turn on ReadyRig in System Settings > Privacy & Security > Accessibility, then restart ReadyRig."
		}
	}
	return ""
}
func (c *Computer) settleInterval() time.Duration {
	if c.interval > 0 {
		return c.interval
	}
	return 150 * time.Millisecond
}

var pair = func(description string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "minItems": 2, "maxItems": 2, "description": description}
}
var actionNames = []string{"mouse_move", "left_click", "right_click", "middle_click", "double_click", "triple_click", "drag", "scroll", "type", "paste", "key", "wait"}

func stepProps() map[string]any {
	return map[string]any{
		"action":       map[string]any{"type": "string", "description": "What to do", "enum": actionNames},
		"coordinate":   pair("[x,y] pixels in the frame_id screenshot"),
		"to":           pair("Drag end [x,y]"),
		"element":      harness.Prop("string", "Ref (e12) from computer_ui_tree, instead of a coordinate"),
		"text":         harness.Prop("string", "Text to type, or to paste via the clipboard (best for long or non-ASCII text)"),
		"keys":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 5, "description": "One chord: modifiers plus one key, e.g. [\"cmd\",\"c\"], [\"enter\"]"},
		"scroll_delta": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "minItems": 2, "maxItems": 2, "description": "[horizontal,vertical] wheel pixels, at coordinate/element if given"},
		"modifiers":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Held during click/drag/scroll: cmd, ctrl, alt, shift"},
		"duration_ms":  map[string]any{"type": "integer", "minimum": 1, "maximum": 10000, "description": "Pause for wait"},
		"frame_id":     harness.Prop("string", "Screenshot the coordinates refer to"),
	}
}
func (c *Computer) Register(r *harness.Registry) {
	r.Register(harness.Tool{Spec: harness.Spec{Name: "computer_screenshot", Category: "computer", Description: "Capture a display, or one app's window with app, as a JPEG (max 1280 px) and return a frame_id; for image files use read_file. Coordinates in the image are what computer_action expects. region=[x1,y1,x2,y2] with an earlier frame_id zooms in at full resolution. display picks a monitor. Needs Screen Recording permission.", InputSchema: harness.Schema(map[string]any{"display": map[string]any{"type": "integer", "minimum": 1, "maximum": 16, "description": "Monitor number, default 1 (main)"}, "app": harness.Prop("string", "App name substring"), "window": harness.Prop("string", "Window title substring"), "region": map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "minItems": 4, "maxItems": 4, "description": "[x1,y1,x2,y2] area of frame_id to zoom into"}, "frame_id": harness.Prop("string", "Screenshot that region refers to")})}, Run: c.screenshot})
	step := stepProps()
	props := map[string]any{"actions": map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "minItems": 1, "maxItems": maxBatch, "description": "Steps run in order in one call (same fields); one screenshot at the end"}, "capture_after": harness.Prop("boolean", "Screenshot afterwards (default true)"), "settle_ms": map[string]any{"type": "integer", "minimum": 0, "maximum": 5000, "description": "Max wait for the screen to settle (default 1000; 0 = fixed 200 ms)"}}
	for k, v := range step {
		props[k] = v
	}
	r.Register(harness.Tool{Spec: harness.Spec{Name: "computer_action", Category: "computer", Mutating: true, Description: "Act on the screen: mouse_move, left/right/middle/double/triple_click, drag, scroll, type, paste, key, wait. One action, or actions[] (max 25) in one call with one screenshot at the end. Target by coordinate (pixels of a frame_id from this session, under 5 minutes old) or by element ref from computer_ui_tree (more reliable). modifiers are held during clicks and scrolls. The returned screenshot waits for the screen to settle. A pointer in a display corner stops input.", InputSchema: harness.Schema(props)}, Run: c.action})
	r.Register(harness.Tool{Spec: harness.Spec{Name: "computer_ui_tree", Category: "computer", Description: "List the accessibility elements (buttons, fields, menus, text) of an app with refs like e12 that computer_action takes as element. app defaults to the frontmost. Refs last 5 minutes in this session. Needs Accessibility permission.", InputSchema: harness.Schema(map[string]any{"app": harness.Prop("string", "Application name (substring), default frontmost"), "max_nodes": map[string]any{"type": "integer", "minimum": 1, "maximum": 800, "description": "Element limit, default 250"}, "depth": map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "description": "Nesting limit, default 10"}})}, Run: c.uiTree})
	r.Register(harness.Tool{Spec: harness.Spec{Name: "computer_app", Category: "computer", Mutating: true, Description: "windows lists visible windows; open or focus launches an app or brings it to the front by name.", InputSchema: harness.Schema(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"windows", "open", "focus"}, "description": "What to do"}, "app": harness.Prop("string", "Application name for open and focus")}, "action")}, Run: c.app})
	r.Register(harness.Tool{Spec: harness.Spec{Name: "computer_clipboard", Category: "computer", Mutating: true, Description: "get or set the clipboard text (recorded in the activity log). To insert text, computer_action paste is simpler.", InputSchema: harness.Schema(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"get", "set"}, "description": "get or set"}, "text": harness.Prop("string", "Text to place on the clipboard (set)")}, "action")}, Run: c.clipboard})
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// grab captures a display; with settle it repeats until two captures are identical
// (the screen stopped changing) or settle has elapsed.
func (c *Computer) grab(ctx context.Context, display int, settle time.Duration) ([]byte, Bounds, error) {
	if settle <= 0 {
		return c.driver.Capture(ctx, display)
	}
	deadline := time.Now().Add(settle)
	var prev uint64
	first := true
	for {
		if err := sleep(ctx, c.settleInterval()); err != nil {
			return nil, Bounds{}, err
		}
		raw, bounds, err := c.driver.Capture(ctx, display)
		if err != nil {
			return nil, Bounds{}, err
		}
		h := fnv.New64a()
		h.Write(raw)
		sum := h.Sum64()
		if !first && sum == prev || time.Now().After(deadline) {
			return raw, bounds, nil
		}
		prev, first = sum, false
	}
}

type captureOptions struct {
	Display int
	Region  []float64
	FrameID string
	App     string
	Window  string
	Settle  time.Duration
}

func (c *Computer) lookup(session, id string) (frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.frames[id]
	if !ok || f.Session != session || time.Since(f.At) > frameLifetime {
		return frame{}, errors.New("take a fresh screenshot in this session before coordinate actions")
	}
	return f, nil
}
func (c *Computer) screenshot(ctx context.Context, in harness.Invocation) (harness.Output, error) {
	var a struct {
		Display int       `json:"display"`
		Region  []float64 `json:"region"`
		FrameID string    `json:"frame_id"`
		App     string    `json:"app"`
		Window  string    `json:"window"`
	}
	if err := harness.Decode(in.Arguments, &a); err != nil {
		return harness.Output{}, err
	}
	if a.Window != "" && a.App == "" {
		return harness.Output{}, errors.New("window needs app")
	}
	if a.App != "" && (len(a.Region) > 0 || a.Display != 0) {
		return harness.Output{}, errors.New("app finds the window and its display; leave out display and region (zoom with region on the frame it returns)")
	}
	if len(a.App) > 200 || len(a.Window) > 200 {
		return harness.Output{}, errors.New("app and window are at most 200 bytes")
	}
	if len(a.Region) > 0 && a.FrameID == "" {
		return harness.Output{}, errors.New("region needs the frame_id it refers to")
	}
	if len(a.Region) == 0 && a.FrameID != "" {
		return harness.Output{}, errors.New("frame_id is only used with region")
	}
	return c.capture(ctx, in, captureOptions{Display: a.Display, Region: a.Region, FrameID: a.FrameID, App: a.App, Window: a.Window})
}

// findWindow picks the frontmost window of app whose title contains title (both
// case-insensitive substrings), preferring real windows over tiny helper ones. It
// returns the display (1-based) showing most of it and the apps of windows in front
// that overlap it.
func (c *Computer) findWindow(ctx context.Context, app, title string) (Window, int, []string, error) {
	windows, err := c.driver.Windows(ctx)
	if err != nil {
		return Window{}, 0, nil, err
	}
	app, title = strings.ToLower(app), strings.ToLower(title)
	tiny := func(w Window) bool { return w.Width < 40 || w.Height < 40 }
	pick := -1
	for i, w := range windows {
		if !strings.Contains(strings.ToLower(w.App), app) || !strings.Contains(strings.ToLower(w.Title), title) {
			continue
		}
		if pick < 0 || tiny(windows[pick]) && !tiny(w) {
			pick = i
		}
	}
	if pick < 0 {
		if title != "" {
			return Window{}, 0, nil, fmt.Errorf("no visible %q window titled like %q; computer_app windows lists them", app, title)
		}
		return Window{}, 0, nil, fmt.Errorf("no visible window of %q; computer_app windows lists them", app)
	}
	w := windows[pick]
	area := func(b Bounds) float64 {
		dx := min(w.X+w.Width, b.X+b.Width) - max(w.X, b.X)
		dy := min(w.Y+w.Height, b.Y+b.Height) - max(w.Y, b.Y)
		return max(dx, 0) * max(dy, 0)
	}
	displays := c.driver.Displays()
	if len(displays) == 0 {
		return Window{}, 0, nil, errors.New("no active display: the screen is asleep or the lid is closed")
	}
	display, best := 0, 0.0
	for i, b := range displays {
		if a := area(b); a > best {
			display, best = i+1, a
		}
	}
	if display == 0 {
		return Window{}, 0, nil, fmt.Errorf("the %s window is not on any display", w.App)
	}
	var covered []string
	for _, o := range windows[:pick] {
		if area(Bounds{o.X, o.Y, o.Width, o.Height}) > 0 && !slices.Contains(covered, o.App) {
			covered = append(covered, o.App)
		}
	}
	return w, display, covered, nil
}
func (c *Computer) capture(ctx context.Context, in harness.Invocation, o captureOptions) (harness.Output, error) {
	display := o.Display
	var parent frame
	zoom := len(o.Region) > 0
	if zoom {
		var err error
		if parent, err = c.lookup(in.Session, o.FrameID); err != nil {
			return harness.Output{}, err
		}
		r := o.Region
		if len(r) != 4 || r[0] < 0 || r[1] < 0 || r[2] <= r[0] || r[3] <= r[1] || r[2] > float64(parent.Width) || r[3] > float64(parent.Height) {
			return harness.Output{}, errors.New("region must be [x1,y1,x2,y2] inside the frame")
		}
		display = parent.Display
	}
	var win *Window
	var covered []string
	if o.App != "" {
		w, d, cov, err := c.findWindow(ctx, o.App, o.Window)
		if err != nil {
			return harness.Output{}, err
		}
		if w.ID == 0 {
			return harness.Output{}, fmt.Errorf("the %s window has no window number to capture", w.App)
		}
		win, display, covered = &w, d, cov
	}
	// A window is captured on its own, so windows in front of it do not show, and a
	// region of a window frame is cut from a new capture of that window.
	winID, winBounds := 0, Bounds{}
	if win != nil {
		winID, winBounds = win.ID, Bounds{win.X, win.Y, win.Width, win.Height}
	} else if zoom {
		winID, winBounds = parent.WindowID, parent.WindowBounds
	}
	if display == 0 {
		display = 1
	}
	var raw []byte
	var bounds Bounds
	var err error
	if winID != 0 {
		raw, err = c.driver.CaptureWindow(ctx, winID)
		bounds = winBounds
	} else {
		raw, bounds, err = c.grab(ctx, display, o.Settle)
	}
	if err != nil {
		return harness.Output{}, err
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return harness.Output{}, err
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	if w == 0 || h == 0 || bounds.Width <= 0 || bounds.Height <= 0 {
		return harness.Output{}, errors.New("empty capture")
	}
	origin := [2]float64{bounds.X, bounds.Y}
	span := [2]float64{bounds.Width, bounds.Height}
	crop := src.Bounds()
	if zoom {
		r := o.Region
		kx, ky := parent.Span[0]/float64(parent.Width), parent.Span[1]/float64(parent.Height)
		origin = [2]float64{parent.Origin[0] + r[0]*kx, parent.Origin[1] + r[1]*ky}
		span = [2]float64{(r[2] - r[0]) * kx, (r[3] - r[1]) * ky}
		sx, sy := float64(w)/bounds.Width, float64(h)/bounds.Height
		crop = image.Rect(int(math.Floor((origin[0]-bounds.X)*sx)), int(math.Floor((origin[1]-bounds.Y)*sy)), int(math.Ceil((origin[0]+span[0]-bounds.X)*sx)), int(math.Ceil((origin[1]+span[1]-bounds.Y)*sy))).Intersect(src.Bounds())
		if crop.Empty() {
			return harness.Output{}, errors.New("region is outside the display")
		}
	}
	ow, oh := crop.Dx(), crop.Dy()
	if max(ow, oh) > maxEdge {
		scale := float64(maxEdge) / float64(max(ow, oh))
		ow, oh = max(1, int(float64(ow)*scale)), max(1, int(float64(oh)*scale))
	}
	dst := image.NewRGBA(image.Rect(0, 0, ow, oh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, draw.Over, nil)
	var buf bytes.Buffer
	if err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return harness.Output{}, err
	}
	if err = ctx.Err(); err != nil {
		return harness.Output{}, err
	}
	id := harness.ID()
	filename := id + ".jpg"
	if err = os.WriteFile(filepath.Join(c.dir, filename), buf.Bytes(), 0600); err != nil {
		return harness.Output{}, err
	}
	c.mu.Lock()
	for key, f := range c.frames {
		if time.Since(f.At) > frameLifetime {
			delete(c.frames, key)
		}
	}
	c.frames[id] = frame{Width: ow, Height: oh, Origin: origin, Span: span, Display: display, At: time.Now(), Session: in.Session, WindowID: winID, WindowBounds: winBounds}
	c.mu.Unlock()
	screen := bounds
	if ds := c.driver.Displays(); winID != 0 && display <= len(ds) {
		screen = ds[display-1]
	}
	value := map[string]any{"status": "success", "frame_id": id, "image_size": []int{ow, oh}, "screen_size": []float64{screen.Width, screen.Height}, "display": display, "displays": len(c.driver.Displays()), "screenshot": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())}
	if zoom {
		value["zoomed"] = true
	}
	if win != nil {
		value["window"] = map[string]any{"app": win.App, "title": win.Title}
		if len(covered) > 0 {
			value["covered_by"] = covered
		}
	}
	return harness.Output{Screenshot: filename, Value: value}, nil
}

var modifierBits = map[string]uint64{"cmd": 1 << 20, "command": 1 << 20, "meta": 1 << 20, "super": 1 << 20, "ctrl": 1 << 18, "control": 1 << 18, "alt": 1 << 19, "option": 1 << 19, "shift": 1 << 17}

func modifierFlags(names []string) (uint64, error) {
	var flags uint64
	for _, n := range names {
		bit, ok := modifierBits[strings.ToLower(n)]
		if !ok {
			return 0, fmt.Errorf("unknown modifier %q (use cmd, ctrl, alt, shift)", n)
		}
		flags |= bit
	}
	return flags, nil
}

// toPoint maps a pixel of frame f to global display points.
func toPoint(f frame, p []float64) ([]float64, error) {
	if len(p) != 2 || p[0] < 0 || p[1] < 0 || p[0] >= float64(f.Width) || p[1] >= float64(f.Height) {
		return nil, errors.New("coordinate outside screenshot")
	}
	return []float64{f.Origin[0] + p[0]*f.Span[0]/float64(f.Width), f.Origin[1] + p[1]*f.Span[1]/float64(f.Height)}, nil
}
func (c *Computer) elementPoint(session, ref string) ([]float64, error) {
	c.mu.Lock()
	st, ok := c.trees[session]
	c.mu.Unlock()
	if !ok || time.Since(st.at) > frameLifetime {
		return nil, errors.New("no recent computer_ui_tree in this session; list the elements first")
	}
	for _, n := range st.nodes {
		if n.Ref == ref {
			if n.Width <= 0 || n.Height <= 0 {
				return nil, fmt.Errorf("element %s has no on-screen position", ref)
			}
			return []float64{n.X + n.Width/2, n.Y + n.Height/2}, nil
		}
	}
	return nil, fmt.Errorf("unknown element %q; list the elements again", ref)
}

// prepare validates one step and resolves its targets to global display points.
func (c *Computer) prepare(a Action, session, defaultFrame string) (Action, error) {
	if a.FrameID == "" {
		a.FrameID = defaultFrame
	}
	var err error
	if a.Flags, err = modifierFlags(a.Modifiers); err != nil {
		return a, err
	}
	target := func(optional bool) error {
		switch {
		case a.Element != "":
			a.Coordinate, err = c.elementPoint(session, a.Element)
			return err
		case len(a.Coordinate) == 0 && optional:
			return nil
		}
		f, err := c.lookup(session, a.FrameID)
		if err != nil {
			return err
		}
		a.Coordinate, err = toPoint(f, a.Coordinate)
		return err
	}
	switch a.Action {
	case "mouse_move", "left_click", "right_click", "middle_click", "double_click", "triple_click":
		if err = target(false); err != nil {
			return a, err
		}
	case "drag":
		if err = target(false); err != nil {
			return a, err
		}
		f, e := c.lookup(session, a.FrameID)
		if e != nil {
			return a, e
		}
		if a.To, err = toPoint(f, a.To); err != nil {
			return a, err
		}
	case "scroll":
		if len(a.Scroll) != 2 || a.Scroll[0] < -10000 || a.Scroll[0] > 10000 || a.Scroll[1] < -10000 || a.Scroll[1] > 10000 {
			return a, errors.New("scroll_delta must contain two values between -10000 and 10000")
		}
		if err = target(true); err != nil {
			return a, err
		}
	case "type", "paste":
		if a.Text == "" || len(a.Text) > maxTextBytes {
			return a, fmt.Errorf("%s requires 1-%d bytes of text", a.Action, maxTextBytes)
		}
	case "key":
		if len(a.Keys) < 1 || len(a.Keys) > 5 {
			return a, errors.New("key requires 1-5 keys")
		}
	case "wait":
		if a.DurationMS < 1 || a.DurationMS > 10000 {
			return a, errors.New("wait requires duration_ms between 1 and 10000")
		}
	case "":
		return a, errors.New("action is required")
	default:
		return a, fmt.Errorf("unknown action %q", a.Action)
	}
	return a, nil
}

// run performs one prepared step.
func (c *Computer) run(ctx context.Context, a Action) error {
	switch a.Action {
	case "wait":
		return sleep(ctx, time.Duration(a.DurationMS)*time.Millisecond)
	case "paste":
		// Borrow the clipboard and put the user's text back afterwards.
		previous, readErr := c.driver.Clipboard(ctx, nil)
		if _, err := c.driver.Clipboard(ctx, &a.Text); err != nil {
			return err
		}
		err := c.driver.Input(ctx, Action{Action: "key", Keys: []string{"cmd", "v"}})
		if err == nil {
			err = sleep(ctx, 250*time.Millisecond)
		}
		if readErr == nil {
			c.driver.Clipboard(context.WithoutCancel(ctx), &previous)
		}
		return err
	}
	return c.driver.Input(ctx, a)
}

type request struct {
	Action
	Actions      []Action `json:"actions"`
	CaptureAfter *bool    `json:"capture_after"`
	SettleMS     *int     `json:"settle_ms"`
}

func (c *Computer) action(ctx context.Context, in harness.Invocation) (harness.Output, error) {
	var req request
	if err := harness.Decode(in.Arguments, &req); err != nil {
		return harness.Output{}, err
	}
	steps := req.Actions
	switch {
	case req.Action.Action != "" && len(steps) > 0:
		return harness.Output{}, errors.New("use either action or actions, not both")
	case req.Action.Action != "":
		steps = []Action{req.Action}
	case len(steps) == 0:
		return harness.Output{}, errors.New("action or actions is required")
	case len(steps) > maxBatch:
		return harness.Output{}, fmt.Errorf("at most %d actions per call", maxBatch)
	}
	// Validate every step before touching the machine, so a typo in step 5 cannot
	// leave steps 1-4 half done.
	prepared := make([]Action, len(steps))
	for i, s := range steps {
		p, err := c.prepare(s, in.Session, req.Action.FrameID)
		if err != nil {
			if len(steps) > 1 {
				err = fmt.Errorf("step %d: %w", i+1, err)
			}
			return harness.Output{}, err
		}
		prepared[i] = p
	}
	for i, p := range prepared {
		if i > 0 {
			if err := sleep(ctx, 100*time.Millisecond); err != nil {
				return harness.Output{Value: map[string]any{"status": "error", "completed": i, "steps": len(prepared)}}, err
			}
		}
		if err := c.run(ctx, p); err != nil {
			return harness.Output{Value: map[string]any{"status": "error", "completed": i, "steps": len(prepared)}}, fmt.Errorf("step %d (%s): %w", i+1, p.Action, err)
		}
	}
	if req.CaptureAfter != nil && !*req.CaptureAfter {
		return harness.Output{Value: map[string]any{"status": "success", "action": prepared[len(prepared)-1].Action, "steps": len(prepared)}}, nil
	}
	settle := defaultSettle
	if req.SettleMS != nil {
		settle = *req.SettleMS
	}
	if settle == 0 {
		if err := sleep(ctx, 200*time.Millisecond); err != nil {
			return harness.Output{}, err
		}
	}
	out, err := c.capture(ctx, in, captureOptions{Settle: time.Duration(settle) * time.Millisecond})
	if err == nil {
		out.Value.(map[string]any)["steps"] = len(prepared)
	}
	return out, err
}

func (c *Computer) clipboard(ctx context.Context, in harness.Invocation) (harness.Output, error) {
	var a struct {
		Action string  `json:"action"`
		Text   *string `json:"text"`
	}
	if err := harness.Decode(in.Arguments, &a); err != nil {
		return harness.Output{}, err
	}
	switch a.Action {
	case "get":
		text, err := c.driver.Clipboard(ctx, nil)
		if err != nil {
			return harness.Output{}, err
		}
		truncated := len(text) > maxClipboard
		if truncated {
			text = text[:maxClipboard]
		}
		return harness.Output{Value: map[string]any{"text": text, "length": len(text), "truncated": truncated}, Text: text, TextKeys: []string{"text"}}, nil
	case "set":
		if a.Text == nil || len(*a.Text) > maxClipboard {
			return harness.Output{}, errors.New("set requires text of at most 1 MiB")
		}
		if _, err := c.driver.Clipboard(ctx, a.Text); err != nil {
			return harness.Output{}, err
		}
		return harness.Output{Value: map[string]any{"status": "success", "length": len(*a.Text)}}, nil
	}
	return harness.Output{}, errors.New("action must be get or set")
}
func (c *Computer) app(ctx context.Context, in harness.Invocation) (harness.Output, error) {
	var a struct {
		Action string `json:"action"`
		App    string `json:"app"`
	}
	if err := harness.Decode(in.Arguments, &a); err != nil {
		return harness.Output{}, err
	}
	switch a.Action {
	case "windows":
		windows, err := c.driver.Windows(ctx)
		if err != nil {
			return harness.Output{}, err
		}
		var text bytes.Buffer
		for _, w := range windows {
			mark := ""
			if w.Frontmost {
				mark = " (frontmost)"
			}
			title := w.Title
			if title == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(&text, "%s — %s [%.0f,%.0f %.0fx%.0f]%s\n", w.App, title, w.X, w.Y, w.Width, w.Height, mark)
		}
		if text.Len() == 0 {
			text.WriteString("[no windows]")
		}
		return harness.Output{Value: map[string]any{"windows": windows}, Text: text.String(), TextKeys: []string{"windows"}}, nil
	case "open", "focus":
		if a.App == "" || len(a.App) > 200 {
			return harness.Output{}, errors.New("app is required")
		}
		if err := c.driver.OpenApp(ctx, a.App); err != nil {
			return harness.Output{}, err
		}
		if err := sleep(ctx, 400*time.Millisecond); err != nil {
			return harness.Output{}, err
		}
		return harness.Output{Value: map[string]any{"status": "success", "app": a.App}}, nil
	}
	return harness.Output{}, errors.New("action must be windows, open or focus")
}
func (c *Computer) uiTree(ctx context.Context, in harness.Invocation) (harness.Output, error) {
	var a struct {
		App      string `json:"app"`
		MaxNodes int    `json:"max_nodes"`
		Depth    int    `json:"depth"`
	}
	if err := harness.Decode(in.Arguments, &a); err != nil {
		return harness.Output{}, err
	}
	if a.MaxNodes == 0 {
		a.MaxNodes = 250
	}
	if a.Depth == 0 {
		a.Depth = 10
	}
	if a.MaxNodes < 1 || a.MaxNodes > 800 || a.Depth < 1 || a.Depth > 20 {
		return harness.Output{}, errors.New("max_nodes must be 1-800 and depth 1-20")
	}
	tree, err := c.driver.UITree(ctx, a.App, a.MaxNodes, a.Depth)
	if err != nil {
		return harness.Output{}, err
	}
	var text bytes.Buffer
	fmt.Fprintf(&text, "app: %s (pid %d), %d elements; pass a ref as element to computer_action\n", tree.App, tree.PID, len(tree.Nodes))
	for i := range tree.Nodes {
		n := &tree.Nodes[i]
		n.Ref = fmt.Sprintf("e%d", i+1)
		fmt.Fprintf(&text, "%*s%s %s", n.Depth*2, "", n.Ref, n.Role)
		if n.Title != "" {
			fmt.Fprintf(&text, " %q", n.Title)
		}
		if n.Description != "" && n.Description != n.Title {
			fmt.Fprintf(&text, " desc=%q", n.Description)
		}
		if n.Value != "" {
			fmt.Fprintf(&text, " value=%q", n.Value)
		}
		fmt.Fprintf(&text, " [%.0f,%.0f %.0fx%.0f]\n", n.X, n.Y, n.Width, n.Height)
	}
	if tree.Truncated {
		text.WriteString("[element limit reached; raise max_nodes or target a narrower app]\n")
	}
	c.mu.Lock()
	c.trees[in.Session] = treeState{nodes: tree.Nodes, at: time.Now()}
	c.mu.Unlock()
	return harness.Output{Value: map[string]any{"app": tree.App, "pid": tree.PID, "nodes": tree.Nodes, "truncated": tree.Truncated}, Text: text.String(), TextKeys: []string{"nodes"}}, nil
}
