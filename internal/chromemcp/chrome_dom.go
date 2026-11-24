package chromemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type axValue struct {
	Value json.RawMessage `json:"value"`
}

func (v *axValue) String() string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v.Value, &s) == nil {
		return s
	}
	return string(v.Value)
}

type axNode struct {
	NodeID      string    `json:"nodeId"`
	Ignored     bool      `json:"ignored"`
	Role        *axValue  `json:"role"`
	Name        *axValue  `json:"name"`
	Value       *axValue  `json:"value"`
	Description *axValue  `json:"description"`
	ParentID    string    `json:"parentId"`
	ChildIDs    []string  `json:"childIds"`
	BackendID   int64     `json:"backendDOMNodeId"`
	Properties  []axProp  `json:"properties"`
	children    []*axNode `json:"-"`
	parent      *axNode   `json:"-"`
}

type axProp struct {
	Name  string  `json:"name"`
	Value axValue `json:"value"`
}

// Properties worth a place in the default snapshot; verbose shows them all.
var axShown = map[string]bool{
	"focused": true, "disabled": true, "expanded": true, "checked": true, "selected": true,
	"required": true, "readonly": true, "pressed": true, "level": true, "url": true,
	"hasPopup": true, "invalid": true, "multiselectable": true, "modal": true,
}

// Booleans whose false value still says something: the control can be toggled.
var axShowFalse = map[string]bool{"expanded": true, "checked": true, "pressed": true}

func axTree(nodes []*axNode) *axNode {
	byID := make(map[string]*axNode, len(nodes))
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	var root *axNode
	for _, n := range nodes {
		for _, id := range n.ChildIDs {
			if child := byID[id]; child != nil {
				n.children = append(n.children, child)
				child.parent = n
			}
		}
		if root == nil && n.ParentID == "" {
			root = n
		}
	}
	return root
}

func quoteAX(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

type snapshotter struct {
	p       *page
	ctx     context.Context
	verbose bool
	prefix  string
	uids    map[string]int64
	out     strings.Builder
	frames  int
}

// shown reports whether a node gets its own line; the children of a node
// without one move up to its parent.
func (s *snapshotter) shown(n *axNode, parent *axNode) bool {
	if s.verbose {
		return true
	}
	if n.Ignored {
		return false
	}
	role, name := n.Role.String(), n.Name.String()
	switch role {
	case "InlineTextBox", "LineBreak", "LabelText":
		return false
	case "none", "generic", "", "presentation":
		return name != "" || n.Value.String() != ""
	case "StaticText":
		if name == "" {
			return false
		}
		// A label's text is the name of the control inside it.
		if n.parent != nil && n.parent.Role.String() == "LabelText" {
			for _, sibling := range n.parent.children {
				if role := sibling.Role.String(); !sibling.Ignored && role != "StaticText" && role != "InlineTextBox" {
					return false
				}
			}
		}
		// The parent's name or value already carries this text.
		same := func(v *axValue) bool {
			return strings.Join(strings.Fields(v.String()), " ") == strings.Join(strings.Fields(name), " ")
		}
		return parent == nil || !same(parent.Name) && !same(parent.Value)
	}
	return true
}

func (s *snapshotter) write(n *axNode, parent *axNode, depth int) {
	if n == nil {
		return
	}
	if !s.shown(n, parent) {
		for _, child := range n.children {
			s.write(child, parent, depth)
		}
		return
	}
	uid := fmt.Sprintf("%s_%d", s.prefix, len(s.uids))
	s.uids[uid] = n.BackendID
	role := n.Role.String()
	if role == "" {
		role = "none"
	}
	if n.Ignored {
		role = "ignored"
	}
	fmt.Fprintf(&s.out, "%suid=%s %s", strings.Repeat("  ", depth), uid, role)
	if name := n.Name.String(); name != "" {
		s.out.WriteString(" " + quoteAX(name))
	}
	if value := n.Value.String(); value != "" {
		s.out.WriteString(" value=" + quoteAX(value))
	}
	if description := n.Description.String(); description != "" && description != n.Name.String() {
		s.out.WriteString(" description=" + quoteAX(description))
	}
	for _, prop := range n.Properties {
		if !s.verbose && !axShown[prop.Name] {
			continue
		}
		switch value := prop.Value.String(); {
		case value == "true":
			s.out.WriteString(" " + prop.Name)
		case value == "false" && !axShowFalse[prop.Name], value == "":
		default:
			s.out.WriteString(" " + prop.Name + "=" + quoteAX(value))
		}
	}
	s.out.WriteByte('\n')
	for _, child := range n.children {
		s.write(child, n, depth+1)
	}
	if (role == "Iframe" || role == "IframePresentational") && n.BackendID != 0 && s.frames < 30 {
		s.frames++
		s.frame(n, depth+1)
	}
}

// frame adds the content of an iframe that lives in the page's own process.
// A cross-site iframe is another process and is left out.
func (s *snapshotter) frame(n *axNode, depth int) {
	var described struct {
		Node struct {
			FrameID string `json:"frameId"`
		} `json:"node"`
	}
	raw, err := s.p.call(s.ctx, "DOM.describeNode", map[string]any{"backendNodeId": n.BackendID})
	if into(raw, err, &described) != nil || described.Node.FrameID == "" {
		return
	}
	root, err := s.p.axRoot(s.ctx, described.Node.FrameID)
	if err != nil || root == nil {
		return
	}
	s.write(root, n, depth)
}

func (p *page) axRoot(ctx context.Context, frame string) (*axNode, error) {
	params := map[string]any{}
	if frame != "" {
		params["frameId"] = frame
	}
	var reply struct {
		Nodes []*axNode `json:"nodes"`
	}
	raw, err := p.call(ctx, "Accessibility.getFullAXTree", params)
	if err = into(raw, err, &reply); err != nil {
		return nil, err
	}
	return axTree(reply.Nodes), nil
}

// keptSnapshots is how many snapshots' uids stay usable. An agent often acts
// on a snapshot it took a few calls ago; a uid whose element is gone fails
// with a clear message when it is used.
const keptSnapshots = 5

// takeSnapshot renders the page's accessibility tree and records its uids.
func (p *page) takeSnapshot(ctx context.Context, verbose bool) (string, error) {
	root, err := p.axRoot(ctx, "")
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	p.snapshot++
	number, gen := p.snapshot, p.gen
	p.mu.Unlock()
	s := &snapshotter{p: p, ctx: ctx, verbose: verbose, prefix: fmt.Sprint(number), uids: map[string]int64{}}
	s.write(root, nil, 0)
	p.mu.Lock()
	if p.gen == gen { // a page that navigated meanwhile has other elements
		if p.uids == nil {
			p.uids = map[int]map[string]int64{}
		}
		p.uids[number] = s.uids
		delete(p.uids, number-keptSnapshots)
	}
	p.mu.Unlock()
	if s.out.Len() == 0 {
		return "(the page has no accessible content yet)\n", nil
	}
	return s.out.String(), nil
}

// node resolves a uid of a recent snapshot to a backend DOM node id.
func (p *page) node(uid string) (int64, error) {
	number := 0
	if prefix, _, ok := strings.Cut(uid, "_"); ok {
		number, _ = strconv.Atoi(prefix)
	}
	p.mu.Lock()
	id, ok := p.uids[number][uid]
	none := len(p.uids) == 0
	p.mu.Unlock()
	switch {
	case none:
		return 0, toolError("No current snapshot for this page. Call chrome_take_snapshot first; uids stop working after a navigation")
	case !ok:
		return 0, toolError("uid %q is not in a recent snapshot of this page. Call chrome_take_snapshot and use its uids", uid)
	case id == 0:
		return 0, toolError("uid %q has no element on the page to act on", uid)
	}
	return id, nil
}

// center scrolls a node into view and returns the middle of its first visible box.
func (p *page) center(ctx context.Context, node int64) (x, y float64, err error) {
	_, _ = p.call(ctx, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": node})
	var quads struct {
		Quads [][]float64 `json:"quads"`
	}
	raw, err := p.call(ctx, "DOM.getContentQuads", map[string]any{"backendNodeId": node})
	if err = into(raw, err, &quads); err == nil {
		for _, q := range quads.Quads {
			if len(q) != 8 {
				continue
			}
			area := ((q[0]*q[3] - q[2]*q[1]) + (q[2]*q[5] - q[4]*q[3]) + (q[4]*q[7] - q[6]*q[5]) + (q[6]*q[1] - q[0]*q[7])) / 2
			if area < 0 {
				area = -area
			}
			if area > 1 {
				return (q[0] + q[2] + q[4] + q[6]) / 4, (q[1] + q[3] + q[5] + q[7]) / 4, nil
			}
		}
	}
	return 0, 0, toolError("The element is not visible on the page, so it cannot be clicked or hovered. Take a new snapshot, or scroll or open what contains it")
}

func (p *page) mouse(ctx context.Context, kind string, x, y float64, extra map[string]any) error {
	params := map[string]any{"type": kind, "x": x, "y": y}
	for k, v := range extra {
		params[k] = v
	}
	return p.act(ctx, "Input.dispatchMouseEvent", params)
}

func (p *page) clickAt(ctx context.Context, x, y float64, double bool) error {
	if err := p.mouse(ctx, "mouseMoved", x, y, nil); err != nil {
		return err
	}
	clicks := 1
	if double {
		clicks = 2
	}
	for n := 1; n <= clicks; n++ {
		if err := p.mouse(ctx, "mousePressed", x, y, map[string]any{"button": "left", "buttons": 1, "clickCount": n}); err != nil {
			return err
		}
		if err := p.mouse(ctx, "mouseReleased", x, y, map[string]any{"button": "left", "clickCount": n}); err != nil {
			return err
		}
	}
	return nil
}

func (p *page) clickNode(ctx context.Context, node int64, double bool) error {
	x, y, err := p.center(ctx, node)
	if err != nil {
		return err
	}
	return p.clickAt(ctx, x, y, double)
}

// object returns a JavaScript handle for a DOM node.
func (p *page) object(ctx context.Context, node int64) (string, error) {
	var reply struct {
		Object remoteObject `json:"object"`
	}
	raw, err := p.call(ctx, "DOM.resolveNode", map[string]any{"backendNodeId": node})
	if err = into(raw, err, &reply); err != nil {
		return "", err
	}
	if reply.Object.ObjectID == "" {
		return "", toolError("The element is no longer on the page. Take a new snapshot")
	}
	return reply.Object.ObjectID, nil
}

type evalReply struct {
	Result           remoteObject      `json:"result"`
	ExceptionDetails *exceptionDetails `json:"exceptionDetails"`
}

// callOn runs a function with a DOM node as this and returns its JSON value.
func (p *page) callOn(ctx context.Context, node int64, function string, args ...any) (json.RawMessage, error) {
	object, err := p.object(ctx, node)
	if err != nil {
		return nil, err
	}
	arguments := make([]map[string]any, len(args))
	for i, a := range args {
		arguments[i] = map[string]any{"value": a}
	}
	var reply evalReply
	raw, err := p.call(ctx, "Runtime.callFunctionOn", map[string]any{"objectId": object, "functionDeclaration": function, "arguments": arguments, "returnByValue": true, "awaitPromise": true})
	if err = into(raw, err, &reply); err != nil {
		return nil, err
	}
	if reply.ExceptionDetails != nil {
		return nil, toolError("%s", reply.ExceptionDetails.message())
	}
	return reply.Result.Value, nil
}

// fillScript prepares an element for a value. It answers "done" when it set the
// value itself, "click" when a click toggles it, "type" when the element is
// focused with its content selected, or "error:<reason>".
const fillScript = `function(value) {
  let el = this.nodeType === 1 ? this : this.parentElement;
  if (!el) return 'error:the element is gone';
  const fire = (target) => { target.dispatchEvent(new Event('input', {bubbles: true})); target.dispatchEvent(new Event('change', {bubbles: true})); };
  if (el.tagName === 'OPTION' && el.parentElement && el.closest('select')) { value = el.value; el = el.closest('select'); }
  if (el.tagName === 'SELECT') {
    const options = [...el.options], wanted = String(value).trim();
    const option = options.find(o => o.value === value) || options.find(o => o.label.trim() === wanted || o.text.trim() === wanted);
    if (!option) return 'error:no option matches ' + JSON.stringify(value) + '; the options are ' + JSON.stringify(options.map(o => o.text.trim()));
    option.selected = true; fire(el); return 'done';
  }
  const role = el.getAttribute('role');
  if (el.tagName === 'INPUT' && (el.type === 'checkbox' || el.type === 'radio')) return el.checked === (value === 'true') ? 'done' : 'click';
  if (role === 'checkbox' || role === 'switch' || role === 'radio' || role === 'menuitemcheckbox' || role === 'menuitemradio') return (el.getAttribute('aria-checked') === 'true') === (value === 'true') ? 'done' : 'click';
  if (el.tagName === 'INPUT' && el.type === 'file') return 'error:this is a file input; use chrome_upload_file';
  if (el.tagName === 'INPUT' && ['date', 'time', 'datetime-local', 'month', 'week', 'color', 'range'].includes(el.type)) {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, value);
    fire(el); return 'done';
  }
  if (el.disabled || el.readOnly) return 'error:the element is disabled or read-only';
  el.focus();
  if (el.isContentEditable) {
    const range = document.createRange(); range.selectNodeContents(el);
    const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range);
    return 'type';
  }
  if (typeof el.select === 'function') { el.select(); return 'type'; }
  return 'error:this element does not take text; it is a ' + el.tagName.toLowerCase();
}`

func (p *page) fill(ctx context.Context, uid, value string) error {
	node, err := p.node(uid)
	if err != nil {
		return err
	}
	_, _ = p.call(ctx, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": node})
	raw, err := p.callOn(ctx, node, fillScript, value)
	if err != nil {
		return err
	}
	var step string
	_ = json.Unmarshal(raw, &step)
	switch {
	case step == "done":
		return nil
	case step == "click":
		return p.clickNode(ctx, node, false)
	case step == "type":
		if value == "" {
			return p.pressKey(ctx, "Backspace")
		}
		return p.act(ctx, "Input.insertText", map[string]any{"text": value})
	}
	return toolError("Cannot fill %s: %s", uid, strings.TrimPrefix(step, "error:"))
}

type keyDef struct {
	key, code string
	vk        int
	text      string
}

var namedKeys = map[string]keyDef{
	"enter": {"Enter", "Enter", 13, "\r"}, "return": {"Enter", "Enter", 13, "\r"}, "tab": {"Tab", "Tab", 9, ""},
	"escape": {"Escape", "Escape", 27, ""}, "esc": {"Escape", "Escape", 27, ""},
	"backspace": {"Backspace", "Backspace", 8, ""}, "delete": {"Delete", "Delete", 46, ""}, "del": {"Delete", "Delete", 46, ""},
	"insert": {"Insert", "Insert", 45, ""}, "home": {"Home", "Home", 36, ""}, "end": {"End", "End", 35, ""},
	"pageup": {"PageUp", "PageUp", 33, ""}, "pagedown": {"PageDown", "PageDown", 34, ""},
	"arrowleft": {"ArrowLeft", "ArrowLeft", 37, ""}, "left": {"ArrowLeft", "ArrowLeft", 37, ""},
	"arrowup": {"ArrowUp", "ArrowUp", 38, ""}, "up": {"ArrowUp", "ArrowUp", 38, ""},
	"arrowright": {"ArrowRight", "ArrowRight", 39, ""}, "right": {"ArrowRight", "ArrowRight", 39, ""},
	"arrowdown": {"ArrowDown", "ArrowDown", 40, ""}, "down": {"ArrowDown", "ArrowDown", 40, ""},
	"space": {" ", "Space", 32, " "}, "spacebar": {" ", "Space", 32, " "},
}

var punctuation = map[rune]keyDef{
	'-': {"-", "Minus", 189, "-"}, '=': {"=", "Equal", 187, "="}, '+': {"+", "Equal", 187, "+"},
	'[': {"[", "BracketLeft", 219, "["}, ']': {"]", "BracketRight", 221, "]"}, '\\': {"\\", "Backslash", 220, "\\"},
	';': {";", "Semicolon", 186, ";"}, '\'': {"'", "Quote", 222, "'"}, ',': {",", "Comma", 188, ","},
	'.': {".", "Period", 190, "."}, '/': {"/", "Slash", 191, "/"}, '`': {"`", "Backquote", 192, "`"}, ' ': {" ", "Space", 32, " "},
}

// Modifier bits as the DevTools protocol numbers them.
const (
	modAlt = 1 << iota
	modControl
	modMeta
	modShift
)

var modifierKeys = map[string]struct {
	bit int
	def keyDef
}{
	"alt": {modAlt, keyDef{"Alt", "AltLeft", 18, ""}}, "option": {modAlt, keyDef{"Alt", "AltLeft", 18, ""}},
	"control": {modControl, keyDef{"Control", "ControlLeft", 17, ""}}, "ctrl": {modControl, keyDef{"Control", "ControlLeft", 17, ""}},
	"meta": {modMeta, keyDef{"Meta", "MetaLeft", 91, ""}}, "cmd": {modMeta, keyDef{"Meta", "MetaLeft", 91, ""}}, "command": {modMeta, keyDef{"Meta", "MetaLeft", 91, ""}},
	"shift": {modShift, keyDef{"Shift", "ShiftLeft", 16, ""}},
}

func lookupKey(name string, shift bool) (keyDef, bool) {
	lower := strings.ToLower(name)
	if def, ok := namedKeys[lower]; ok {
		return def, true
	}
	if len(lower) >= 2 && len(lower) <= 3 && lower[0] == 'f' {
		var n int
		if _, err := fmt.Sscanf(lower[1:], "%d", &n); err == nil && n >= 1 && n <= 12 && fmt.Sprint(n) == lower[1:] {
			return keyDef{"F" + lower[1:], "F" + lower[1:], 111 + n, ""}, true
		}
	}
	if utf8.RuneCountInString(name) != 1 {
		return keyDef{}, false
	}
	r, _ := utf8.DecodeRuneInString(name)
	switch {
	case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
		upper := strings.ToUpper(name)
		key := strings.ToLower(name)
		if shift || r <= 'Z' {
			key = upper
		}
		return keyDef{key, "Key" + upper, int(upper[0]), key}, true
	case r >= '0' && r <= '9':
		return keyDef{name, "Digit" + name, int(r), name}, true
	}
	if def, ok := punctuation[r]; ok {
		return def, true
	}
	return keyDef{name, "", 0, name}, true
}

// Editing shortcuts that Chrome on macOS only performs when they are named.
var macCommands = map[string]string{"a": "selectAll", "c": "copy", "v": "paste", "x": "cut", "z": "undo", "Z": "redo"}

// pressKey presses a key or a combination such as "Control+Shift+R".
func (p *page) pressKey(ctx context.Context, combo string) error {
	var parts []string
	switch {
	case combo == "+":
		parts = []string{"+"}
	case strings.HasSuffix(combo, "++"):
		parts = append(strings.Split(strings.TrimSuffix(combo, "++"), "+"), "+")
	default:
		parts = strings.Split(combo, "+")
	}
	modifiers := 0
	var held []keyDef
	for _, name := range parts[:len(parts)-1] {
		lower := strings.ToLower(strings.TrimSpace(name))
		if lower == "controlormeta" || lower == "cmdorctrl" {
			lower = "control"
			if runtime.GOOS == "darwin" {
				lower = "meta"
			}
		}
		mod, ok := modifierKeys[lower]
		if !ok {
			return toolError("Unknown modifier %q in %q. Modifiers are Control, Shift, Alt and Meta", name, combo)
		}
		modifiers |= mod.bit
		held = append(held, mod.def)
		if err := p.act(ctx, "Input.dispatchKeyEvent", map[string]any{"type": "rawKeyDown", "key": mod.def.key, "code": mod.def.code, "windowsVirtualKeyCode": mod.def.vk, "modifiers": modifiers}); err != nil {
			return err
		}
	}
	name := parts[len(parts)-1]
	if name != " " {
		name = strings.TrimSpace(name)
	}
	if mod, ok := modifierKeys[strings.ToLower(name)]; ok && len(parts) == 1 {
		for _, kind := range []string{"rawKeyDown", "keyUp"} {
			if err := p.act(ctx, "Input.dispatchKeyEvent", map[string]any{"type": kind, "key": mod.def.key, "code": mod.def.code, "windowsVirtualKeyCode": mod.def.vk}); err != nil {
				return err
			}
		}
		return nil
	}
	def, ok := lookupKey(name, modifiers&modShift != 0)
	if !ok {
		return toolError("Unknown key %q. Use one character, or a name such as Enter, Tab, Escape, Backspace, Delete, ArrowDown, PageDown, Home, End, Space or F5", name)
	}
	down := map[string]any{"type": "rawKeyDown", "key": def.key, "code": def.code, "windowsVirtualKeyCode": def.vk, "modifiers": modifiers}
	if def.text != "" && modifiers&(modControl|modMeta|modAlt) == 0 {
		down["type"], down["text"] = "keyDown", def.text
	}
	if command := macCommands[def.key]; runtime.GOOS == "darwin" && command != "" && modifiers&^modShift == modMeta {
		down["commands"] = []string{command}
	}
	if err := p.act(ctx, "Input.dispatchKeyEvent", down); err != nil {
		return err
	}
	if err := p.act(ctx, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": def.key, "code": def.code, "windowsVirtualKeyCode": def.vk, "modifiers": modifiers}); err != nil {
		return err
	}
	for i := len(held) - 1; i >= 0; i-- {
		modifiers &^= modifierKeys[strings.ToLower(held[i].key)].bit
		if err := p.act(ctx, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": held[i].key, "code": held[i].code, "windowsVirtualKeyCode": held[i].vk, "modifiers": modifiers}); err != nil {
			return err
		}
	}
	return nil
}

// dragNode drags one element onto another, as a native drag when the page starts one.
func (p *page) dragNode(ctx context.Context, from, to int64) error {
	x1, y1, err := p.center(ctx, from)
	if err != nil {
		return err
	}
	select {
	case <-p.drag:
	default:
	}
	if _, err = p.call(ctx, "Input.setInterceptDrags", map[string]any{"enabled": true}); err != nil {
		return err
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = p.call(clean, "Input.setInterceptDrags", map[string]any{"enabled": false})
	}()
	if err = p.mouse(ctx, "mouseMoved", x1, y1, nil); err != nil {
		return err
	}
	if err = p.mouse(ctx, "mousePressed", x1, y1, map[string]any{"button": "left", "buttons": 1, "clickCount": 1}); err != nil {
		return err
	}
	// The target may have moved when the source was scrolled into view.
	x2, y2, err := p.center(ctx, to)
	if err != nil {
		_ = p.mouse(ctx, "mouseReleased", x1, y1, map[string]any{"button": "left", "clickCount": 1})
		return err
	}
	const steps = 8
	for i := 1; i <= steps; i++ {
		f := float64(i) / steps
		if err = p.mouse(ctx, "mouseMoved", x1+(x2-x1)*f, y1+(y2-y1)*f, map[string]any{"button": "left", "buttons": 1}); err != nil {
			return err
		}
	}
	select {
	case data := <-p.drag:
		for _, kind := range []string{"dragEnter", "dragOver", "drop"} {
			if _, err = p.call(ctx, "Input.dispatchDragEvent", map[string]any{"type": kind, "x": x2, "y": y2, "data": data}); err != nil {
				return err
			}
		}
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.mouse(ctx, "mouseReleased", x2, y2, map[string]any{"button": "left", "clickCount": 1})
}
