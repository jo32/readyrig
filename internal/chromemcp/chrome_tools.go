package chromemcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"computer-use-server/internal/harness"
)

// chromeTool is one built-in Chrome tool, published as chrome_<name>.
type chromeTool struct {
	name, description string
	schema            map[string]any
	readOnly          bool
	advanced          bool // kept out of tools/list; see harness.Spec.Group
	run               func(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error)
}

type props = map[string]any

func object(properties props, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required}
}

func prop(kind, description string) map[string]any {
	return map[string]any{"type": kind, "description": description}
}

func enum(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

func list(items map[string]any, description string) map[string]any {
	return map[string]any{"type": "array", "description": description, "items": items}
}

// with adds the properties most page tools share.
func with(properties props, names ...string) props {
	shared := props{
		"pageId":          prop("integer", "Page id from chrome_list_pages. Default: the selected page"),
		"uid":             prop("string", "Element uid from a recent chrome_take_snapshot of this page"),
		"includeSnapshot": prop("boolean", "Also return a new snapshot of the page"),
		"timeout":         prop("integer", "Longest wait in milliseconds, default 30000"),
		"pageSize":        prop("integer", "Entries per page; default all"),
		"pageIdx":         prop("integer", "Page of entries to return, from 0"),
	}
	for _, name := range names {
		properties[name] = shared[name]
	}
	return properties
}

const pathRule = "Absolute path inside an approved project or the OS temp folder"

func textResult(text string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &harness.ToolError{Code: "invalid_arguments", Message: "Invalid arguments: " + err.Error()}
	}
	return nil
}

func waitLimit(ms int) time.Duration {
	if ms <= 0 {
		return 30 * time.Second
	}
	return min(time.Duration(ms)*time.Millisecond, 110*time.Second)
}

// checkPath keeps the files Chrome tools read and write inside approved
// projects and the OS temp folder.
func (c *chrome) checkPath(path string) error {
	var roots []string
	if c.roots != nil {
		roots = c.roots()
	}
	if path != "" && filepath.IsAbs(path) && (withinRoots(path, roots) || withinRoots(path, []string{os.TempDir()})) {
		return nil
	}
	return &harness.ToolError{Code: "path_outside_project", Message: "File paths must be absolute and inside an approved project (see list_projects) or the OS temp folder"}
}

func (c *chrome) save(path string, data []byte) error {
	if err := c.checkPath(path); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return toolError("Could not save %s: %v", path, err)
	}
	return nil
}

// finish ends a page tool's answer with what the agent must know next: an open
// dialog, or the new snapshot it asked for.
func (p *page) finish(ctx context.Context, text string, snapshot bool) (map[string]any, error) {
	p.mu.Lock()
	dialog := p.dialog
	p.mu.Unlock()
	switch {
	case dialog != nil:
		text += fmt.Sprintf("\n\n## Open dialog\n%s: %s\nThe page is blocked until chrome_handle_dialog answers it.", dialog.Kind, dialog.Message)
		if dialog.Default != "" {
			text += " Default prompt text: " + dialog.Default
		}
	case snapshot:
		snap, err := p.takeSnapshot(ctx, false)
		if err != nil {
			return nil, err
		}
		text += "\n\n## Latest page snapshot\n" + snap
	}
	return textResult(text), nil
}

func (p *page) eval(ctx context.Context, expression string) (json.RawMessage, error) {
	var reply evalReply
	raw, err := p.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true})
	if err = into(raw, err, &reply); err != nil {
		return nil, err
	}
	if reply.ExceptionDetails != nil {
		return nil, toolError("%s", reply.ExceptionDetails.message())
	}
	return reply.Result.Value, nil
}

// location describes where the page is now.
func (p *page) location(ctx context.Context) string {
	p.mu.Lock()
	blocked := p.dialog != nil
	p.mu.Unlock()
	if blocked {
		return "the page"
	}
	short, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var at [2]string
	raw, err := p.eval(short, `[location.href, document.title]`)
	if err != nil || json.Unmarshal(raw, &at) != nil {
		return "the page"
	}
	if at[1] == "" {
		return at[0]
	}
	return fmt.Sprintf("%s (%s)", at[0], at[1])
}

func (c *chrome) pagesText(ctx context.Context) (string, error) {
	targets, err := c.targets(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	selected := c.selected
	c.mu.Unlock()
	var sb strings.Builder
	sb.WriteString("## Pages\n")
	for _, t := range targets {
		fmt.Fprintf(&sb, "%d: %s", t.ID, t.URL)
		if t.Title != "" && t.Title != t.URL {
			fmt.Fprintf(&sb, " (%s)", strings.Join(strings.Fields(t.Title), " "))
		}
		if t.TargetID == selected {
			sb.WriteString(" [selected]")
		}
		sb.WriteByte('\n')
	}
	if len(targets) == 0 {
		sb.WriteString("(no open pages; chrome_new_page opens one)\n")
	}
	return sb.String(), nil
}

// goTo loads a URL and waits for the page to finish loading.
func (p *page) goTo(ctx context.Context, url string, limit time.Duration) (string, error) {
	p.mu.Lock()
	p.loading = true
	p.mu.Unlock()
	var reply struct {
		LoaderID  string `json:"loaderId"`
		ErrorText string `json:"errorText"`
	}
	raw, err := p.call(ctx, "Page.navigate", map[string]any{"url": url})
	err = into(raw, err, &reply)
	if err != nil || reply.ErrorText != "" || reply.LoaderID == "" {
		p.mu.Lock()
		p.loading = false
		p.notify()
		p.mu.Unlock()
	}
	if err != nil {
		return "", toolError("Could not open %s: %v", url, err)
	}
	if reply.ErrorText != "" {
		return "", toolError("Could not open %s: %s", url, reply.ErrorText)
	}
	return p.loaded(ctx, limit), nil
}

// loaded waits for the page to finish loading and says where it ended up.
func (p *page) loaded(ctx context.Context, limit time.Duration) string {
	if !p.wait(ctx, limit, func() bool { return !p.loading || p.dialog != nil }) {
		return fmt.Sprintf("Still loading after %s; now at %s.", limit, p.location(ctx))
	}
	return "Now at " + p.location(ctx) + "."
}

type pageArg struct {
	PageID *int `json:"pageId"`
}

type elementArgs struct {
	pageArg
	UID             string `json:"uid"`
	IncludeSnapshot bool   `json:"includeSnapshot"`
}

// onElement is the common shape of a tool that acts on one snapshot element.
func onElement(done string, action func(ctx context.Context, p *page, node int64, raw json.RawMessage) error) func(context.Context, *chrome, json.RawMessage) (map[string]any, error) {
	return func(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
		var a elementArgs
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		p, err := c.page(ctx, a.PageID)
		if err != nil {
			return nil, err
		}
		node, err := p.node(a.UID)
		if err != nil {
			return nil, err
		}
		if err = action(ctx, p, node, raw); err != nil {
			return nil, err
		}
		p.settle(ctx, 10*time.Second)
		return p.finish(ctx, done, a.IncludeSnapshot)
	}
}

var networkPresets = map[string]map[string]any{
	"No emulation": {"offline": false, "latency": 0, "downloadThroughput": -1, "uploadThroughput": -1},
	"Offline":      {"offline": true, "latency": 0, "downloadThroughput": 0, "uploadThroughput": 0},
	"Slow 3G":      {"offline": false, "latency": 2000, "downloadThroughput": 50000, "uploadThroughput": 50000},
	"Fast 3G":      {"offline": false, "latency": 562.5, "downloadThroughput": 180000, "uploadThroughput": 84375},
	"Slow 4G":      {"offline": false, "latency": 562.5, "downloadThroughput": 180000, "uploadThroughput": 84375},
	"Fast 4G":      {"offline": false, "latency": 165, "downloadThroughput": 1012500, "uploadThroughput": 168750},
}

func headerLines(sb *strings.Builder, headers map[string]any) {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(sb, "- %s: %v\n", name, headers[name])
	}
}

func (r *netRequest) state() string {
	switch {
	case r.failure != "":
		return "failed - " + r.failure
	case r.responded:
		return strconv.Itoa(r.status)
	}
	return "pending"
}

// paginate returns the wanted slice bounds of n entries and a note about the rest.
func paginate(n, size, idx int) (from, to int, note string) {
	if size <= 0 {
		return 0, n, ""
	}
	from = min(max(idx, 0)*size, n)
	to = min(from+size, n)
	return from, to, fmt.Sprintf("Showing %d-%d of %d.\n", from+1, to, n)
}

const inlineBody = 10000

func chromeCatalog() []chromeTool {
	return []chromeTool{
		{name: "list_pages", description: "List the pages (tabs) open in Chrome with their page ids.", schema: object(props{}), readOnly: true,
			run: func(ctx context.Context, c *chrome, _ json.RawMessage) (map[string]any, error) {
				text, err := c.pagesText(ctx)
				if err != nil {
					return nil, err
				}
				return textResult(text), nil
			}},
		{name: "select_page", description: "Select the page that later calls use when they omit pageId.", readOnly: true,
			schema: object(props{"pageId": prop("integer", "Page id from chrome_list_pages"), "bringToFront": prop("boolean", "Also show the tab in Chrome")}, "pageId"),
			run: func(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
				var a struct {
					pageArg
					BringToFront bool `json:"bringToFront"`
				}
				if err := decode(raw, &a); err != nil {
					return nil, err
				}
				if a.PageID == nil {
					return nil, toolError("pageId is required")
				}
				p, err := c.page(ctx, a.PageID)
				if err != nil {
					return nil, err
				}
				c.selectTarget(p.target)
				if a.BringToFront {
					if _, err = p.call(ctx, "Page.bringToFront", nil); err != nil {
						return nil, err
					}
				}
				text, err := c.pagesText(ctx)
				if err != nil {
					return nil, err
				}
				return textResult(text), nil
			}},
		{name: "new_page", description: "Open a URL in a new tab and select it.",
			schema: object(with(props{"url": prop("string", "URL to load"), "background": prop("boolean", "Open the tab without bringing it to the front"),
				"isolatedContext": prop("string", "Name of an isolated browser context to open the tab in. Tabs in one context share cookies and storage; contexts are separate from each other and from the user's profile")}, "timeout"), "url"),
			run: func(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
				var a struct {
					URL             string `json:"url"`
					Background      bool   `json:"background"`
					IsolatedContext string `json:"isolatedContext"`
					Timeout         int    `json:"timeout"`
				}
				if err := decode(raw, &a); err != nil {
					return nil, err
				}
				if a.URL == "" {
					return nil, toolError("url is required")
				}
				create := map[string]any{"url": "about:blank", "background": a.Background}
				if a.IsolatedContext != "" {
					c.mu.Lock()
					isolated := c.contexts[a.IsolatedContext]
					c.mu.Unlock()
					if isolated == "" {
						var made struct {
							BrowserContextID string `json:"browserContextId"`
						}
						res, err := c.conn.call(ctx, "", "Target.createBrowserContext", map[string]any{"disposeOnDetach": true})
						if err = into(res, err, &made); err != nil {
							return nil, err
						}
						isolated = made.BrowserContextID
						c.mu.Lock()
						c.contexts[a.IsolatedContext] = isolated
						c.mu.Unlock()
					}
					create["browserContextId"] = isolated
				}
				var made struct {
					TargetID string `json:"targetId"`
				}
				res, err := c.conn.call(ctx, "", "Target.createTarget", create)
				if err = into(res, err, &made); err != nil {
					return nil, err
				}
				p, err := c.attach(ctx, made.TargetID, c.pageID(made.TargetID))
				if err != nil {
					return nil, err
				}
				c.selectTarget(p.target)
				state, err := p.goTo(ctx, a.URL, waitLimit(a.Timeout))
				if err != nil {
					return nil, err
				}
				return p.finish(ctx, fmt.Sprintf("Opened page %d and selected it. %s", p.id, state), false)
			}},
		{name: "close_page", description: "Close a page. The last open page cannot be closed.",
			schema: object(props{"pageId": prop("integer", "Page id from chrome_list_pages")}, "pageId"),
			run: func(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
				var a pageArg
				if err := decode(raw, &a); err != nil {
					return nil, err
				}
				targets, err := c.targets(ctx)
				if err != nil {
					return nil, err
				}
				for _, t := range targets {
					if a.PageID == nil || t.ID != *a.PageID {
						continue
					}
					if len(targets) == 1 {
						return nil, toolError("The last open page cannot be closed")
					}
					if _, err = c.conn.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": t.TargetID}); err != nil {
						return nil, err
					}
					pause(ctx, 150*time.Millisecond)
					text, err := c.pagesText(ctx)
					if err != nil {
						return nil, err
					}
					return textResult(fmt.Sprintf("Closed page %d.\n\n%s", t.ID, text)), nil
				}
				return nil, toolError("pageId must be a page id from chrome_list_pages")
			}},
		{name: "navigate_page", description: "Go to a URL, or back, forward or reload, and wait for the page to load.",
			schema: object(with(props{"type": enum("Default url", "url", "back", "forward", "reload"), "url": prop("string", "URL to load (type url)"),
				"ignoreCache": prop("boolean", "Reload without the cache"), "handleBeforeUnload": enum("Answer to a \"leave this page?\" dialog, default accept", "accept", "dismiss"),
				"initScript": prop("string", "JavaScript to run in the new document before its own scripts")}, "pageId", "timeout")),
			run: navigatePage},
		{name: "take_snapshot", description: "Read the page as text from its accessibility tree. Each element has a uid that the other tools take; uids stop working when the page navigates. Prefer this over a screenshot. Cross-site iframes are not included.", readOnly: true,
			schema: object(with(props{"verbose": prop("boolean", "Include every node and property of the tree"), "filePath": prop("string", "Save the snapshot here instead of returning it. "+pathRule)}, "pageId")),
			run: func(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
				var a struct {
					pageArg
					Verbose  bool   `json:"verbose"`
					FilePath string `json:"filePath"`
				}
				if err := decode(raw, &a); err != nil {
					return nil, err
				}
				p, err := c.page(ctx, a.PageID)
				if err != nil {
					return nil, err
				}
				snap, err := p.takeSnapshot(ctx, a.Verbose)
				if err != nil {
					return nil, err
				}
				if a.FilePath != "" {
					if err = c.save(a.FilePath, []byte(snap)); err != nil {
						return nil, err
					}
					return textResult("Saved the snapshot to " + a.FilePath), nil
				}
				return textResult("## Latest page snapshot\n" + snap), nil
			}},
		{name: "take_screenshot", description: "Take a picture of the page's visible area, the full page or one element.", readOnly: true,
			schema: object(with(props{"format": enum("Default png", "png", "jpeg", "webp"), "quality": prop("number", "JPEG or WebP quality, 0-100"),
				"fullPage": prop("boolean", "Capture the whole page, not only the visible area"), "filePath": prop("string", "Save the picture here instead of returning it. "+pathRule)}, "pageId", "uid")),
			run: takeScreenshot},
		{name: "click", description: "Click an element.", schema: object(with(props{"dblClick": prop("boolean", "Double click")}, "pageId", "uid", "includeSnapshot"), "uid"),
			run: onElement("Clicked the element.", func(ctx context.Context, p *page, node int64, raw json.RawMessage) error {
				var a struct {
					DblClick bool `json:"dblClick"`
				}
				_ = json.Unmarshal(raw, &a)
				return p.clickNode(ctx, node, a.DblClick)
			})},
		{name: "hover", description: "Move the mouse over an element.", schema: object(with(props{}, "pageId", "uid", "includeSnapshot"), "uid"),
			run: onElement("Hovering over the element.", func(ctx context.Context, p *page, node int64, _ json.RawMessage) error {
				x, y, err := p.center(ctx, node)
				if err != nil {
					return err
				}
				return p.mouse(ctx, "mouseMoved", x, y, nil)
			})},
		{name: "drag", description: "Drag an element onto another element.",
			schema: object(with(props{"from_uid": prop("string", "uid of the element to drag"), "to_uid": prop("string", "uid of the element to drop it on")}, "pageId", "includeSnapshot"), "from_uid", "to_uid"),
			run: func(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
				var a struct {
					pageArg
					From            string `json:"from_uid"`
					To              string `json:"to_uid"`
					IncludeSnapshot bool   `json:"includeSnapshot"`
				}
				if err := decode(raw, &a); err != nil {
					return nil, err
				}
				p, err := c.page(ctx, a.PageID)
				if err != nil {
					return nil, err
				}
				from, err := p.node(a.From)
				if err != nil {
					return nil, err
				}
				to, err := p.node(a.To)
				if err != nil {
					return nil, err
				}
				if err = p.dragNode(ctx, from, to); err != nil {
					return nil, err
				}
				p.settle(ctx, 10*time.Second)
				return p.finish(ctx, "Dragged the element.", a.IncludeSnapshot)
			}},
		{name: "fill", description: "Set the value of a text field, select, checkbox or radio button, replacing what it holds.",
			schema: object(with(props{"value": prop("string", "Text to enter, or the option to select. \"true\" or \"false\" for a checkbox or switch, \"true\" for a radio button")}, "pageId", "uid", "includeSnapshot"), "uid", "value"),
			run: func(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
				var a struct {
					elementArgs
					Value string `json:"value"`
				}
				if err := decode(raw, &a); err != nil {
					return nil, err
				}
				p, err := c.page(ctx, a.PageID)
				if err != nil {
					return nil, err
				}
				if err = p.fill(ctx, a.UID, a.Value); err != nil {
					return nil, err
				}
				p.settle(ctx, 10*time.Second)
				return p.finish(ctx, "Filled the element.", a.IncludeSnapshot)
			}},
		{name: "fill_form", description: "Fill several form elements in one call. Prefer this over repeated chrome_fill calls.",
			schema: object(with(props{"elements": list(object(props{"uid": prop("string", "Element uid"), "value": prop("string", "Value, as for chrome_fill")}, "uid", "value"), "Elements to fill, in order")}, "pageId", "includeSnapshot"), "elements"),
			run: func(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
				var a struct {
					pageArg
					Elements []struct {
						UID   string `json:"uid"`
						Value string `json:"value"`
					} `json:"elements"`
					IncludeSnapshot bool `json:"includeSnapshot"`
				}
				if err := decode(raw, &a); err != nil {
					return nil, err
				}
				if len(a.Elements) == 0 {
					return nil, toolError("elements must list at least one element")
				}
				p, err := c.page(ctx, a.PageID)
				if err != nil {
					return nil, err
				}
				for i, element := range a.Elements {
					if err = p.fill(ctx, element.UID, element.Value); err != nil {
						return nil, toolError("%v (%d of %d elements were filled before this one)", err, i, len(a.Elements))
					}
					pause(ctx, 30*time.Millisecond)
				}
				p.settle(ctx, 10*time.Second)
				return p.finish(ctx, fmt.Sprintf("Filled %d elements.", len(a.Elements)), a.IncludeSnapshot)
			}},
		{name: "type_text", description: "Type text into the element that has the focus, at the cursor. Click a field first to focus it.",
			schema: object(with(props{"text": prop("string", "Text to type"), "submitKey": prop("string", "Key to press afterwards, such as Enter or Tab")}, "pageId"), "text"),
			run: func(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
				var a struct {
					pageArg
					Text      string `json:"text"`
					SubmitKey string `json:"submitKey"`
				}
				if err := decode(raw, &a); err != nil {
					return nil, err
				}
				p, err := c.page(ctx, a.PageID)
				if err != nil {
					return nil, err
				}
				if a.Text != "" {
					if err = p.act(ctx, "Input.insertText", map[string]any{"text": a.Text}); err != nil {
						return nil, err
					}
				}
				if a.SubmitKey != "" {
					if err = p.pressKey(ctx, a.SubmitKey); err != nil {
						return nil, err
					}
				}
				p.settle(ctx, 10*time.Second)
				return p.finish(ctx, "Typed the text.", false)
			}},
		{name: "press_key", description: "Press a key or a combination, for shortcuts and keys that chrome_fill cannot send.",
			schema: object(with(props{"key": prop("string", "A key or combination such as Enter, ArrowDown, Control+A or Control+Shift+R. Modifiers: Control, Shift, Alt, Meta")}, "pageId", "includeSnapshot"), "key"),
			run: func(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
				var a struct {
					pageArg
					Key             string `json:"key"`
					IncludeSnapshot bool   `json:"includeSnapshot"`
				}
				if err := decode(raw, &a); err != nil {
					return nil, err
				}
				if a.Key == "" {
					return nil, toolError("key is required")
				}
				p, err := c.page(ctx, a.PageID)
				if err != nil {
					return nil, err
				}
				if err = p.pressKey(ctx, a.Key); err != nil {
					return nil, err
				}
				p.settle(ctx, 10*time.Second)
				return p.finish(ctx, "Pressed "+a.Key+".", a.IncludeSnapshot)
			}},
		{name: "upload_file", description: "Choose files in a file input, or through the element that opens the file chooser.",
			schema: object(with(props{"filePaths": list(prop("string", pathRule), "Files to upload")}, "pageId", "uid", "includeSnapshot"), "uid", "filePaths"),
			run:    uploadFile},
		{name: "wait_for", description: "Wait until any of the given texts appears on the page, then return a snapshot.", readOnly: true,
			schema: object(with(props{"text": list(prop("string", "Text to look for"), "Texts; the wait ends when one of them is on the page")}, "pageId", "timeout"), "text"),
			run:    waitForText},
		{name: "handle_dialog", description: "Answer the alert, confirm or prompt dialog that is open on a page.",
			schema: object(with(props{"action": enum("What to do with the dialog", "accept", "dismiss"), "promptText": prop("string", "Text to enter in a prompt")}, "pageId"), "action"),
			run: func(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
				var a struct {
					pageArg
					Action     string  `json:"action"`
					PromptText *string `json:"promptText"`
				}
				if err := decode(raw, &a); err != nil {
					return nil, err
				}
				if a.Action != "accept" && a.Action != "dismiss" {
					return nil, toolError("action must be accept or dismiss")
				}
				p, err := c.page(ctx, a.PageID)
				if err != nil {
					return nil, err
				}
				p.mu.Lock()
				open := p.dialog != nil
				p.mu.Unlock()
				if !open {
					return nil, toolError("No dialog is open on this page")
				}
				answer := map[string]any{"accept": a.Action == "accept"}
				if a.PromptText != nil {
					answer["promptText"] = *a.PromptText
				}
				if _, err = p.call(ctx, "Page.handleJavaScriptDialog", answer); err != nil {
					return nil, err
				}
				p.mu.Lock()
				p.dialog = nil
				p.notify()
				p.mu.Unlock()
				p.settle(ctx, 10*time.Second)
				return p.finish(ctx, "Answered the dialog: "+a.Action+".", false)
			}},
		{name: "evaluate_script", description: "Run a JavaScript function in the page and return its result as JSON.",
			schema: object(with(props{"function": prop("string", "Function declaration, e.g. `() => document.title`, `async () => (await fetch('/api')).status` or `(el) => el.innerText` with args"),
				"args": list(prop("string", "Element uid"), "Elements passed to the function, by uid"), "filePath": prop("string", "Save the result here instead of returning it. "+pathRule),
				"dialogAction": prop("string", "Answer to dialogs the script opens: accept (default), dismiss, or the text for a prompt")}, "pageId"), "function"),
			run: evaluateScript},
		{name: "list_console_messages", description: "List the page's console messages since its last navigation. Messages are recorded from the first tool call on a page.", readOnly: true, advanced: true,
			schema: object(with(props{"types": list(prop("string", "log, debug, info, warn, error, ..."), "Only these message types"), "includePreservedMessages": prop("boolean", "Include the last 3 navigations"),
				"includeStackTraces": prop("boolean", "Add each message's stack trace")}, "pageId", "pageSize", "pageIdx")),
			run: listConsole},
		{name: "get_console_message", description: "Get one console message in full, with its stack trace.", readOnly: true, advanced: true,
			schema: object(with(props{"msgid": prop("integer", "msgid from chrome_list_console_messages")}, "pageId"), "msgid"),
			run: func(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
				var a struct {
					pageArg
					MsgID int `json:"msgid"`
				}
				if err := decode(raw, &a); err != nil {
					return nil, err
				}
				p, err := c.page(ctx, a.PageID)
				if err != nil {
					return nil, err
				}
				p.mu.Lock()
				defer p.mu.Unlock()
				for _, m := range p.console {
					if m.id == a.MsgID {
						text := fmt.Sprintf("msgid=%d [%s] %s", m.id, m.kind, m.text)
						if m.stack != "" {
							text += "\n### Stack trace\n" + m.stack
						}
						return textResult(text), nil
					}
				}
				return nil, toolError("No console message with msgid %d", a.MsgID)
			}},
		{name: "list_network_requests", description: "List the page's network requests since its last navigation. Requests are recorded from the first tool call on a page.", readOnly: true, advanced: true,
			schema: object(with(props{"resourceTypes": list(prop("string", "document, stylesheet, image, media, font, script, xhr, fetch, websocket, ..."), "Only these resource types"),
				"includePreservedRequests": prop("boolean", "Include the last 3 navigations")}, "pageId", "pageSize", "pageIdx")),
			run: listNetwork},
		{name: "get_network_request", description: "Get one network request: its headers, including cookies, and its request and response bodies.", readOnly: true, advanced: true,
			schema: object(with(props{"reqid": prop("integer", "reqid from chrome_list_network_requests"), "requestFilePath": prop("string", "Save the request body here. "+pathRule),
				"responseFilePath": prop("string", "Save the response body here. "+pathRule)}, "pageId"), "reqid"),
			run: getNetwork},
		{name: "emulate", description: "Emulate network speed, a slow CPU, a location, a user agent, a color scheme, a device viewport or extra HTTP headers on a page. Properties left out stay as they are.", advanced: true,
			schema: object(with(props{"networkConditions": enum("Network throttling", "No emulation", "Offline", "Slow 3G", "Fast 3G", "Slow 4G", "Fast 4G"),
				"cpuThrottlingRate": prop("number", "CPU slowdown factor, 1-20; 1 turns it off"), "geolocation": prop("string", "\"<latitude>,<longitude>\"; empty clears it"),
				"userAgent": prop("string", "User agent; empty clears it"), "colorScheme": enum("auto resets it", "dark", "light", "auto"),
				"viewport":         prop("string", "\"<width>x<height>x<devicePixelRatio>[,mobile][,touch][,landscape]\"; empty clears it"),
				"extraHttpHeaders": prop("string", "JSON object of headers added to every request of the page; empty clears them")}, "pageId")),
			run: emulate},
		{name: "resize_page", description: "Resize the page's window so that the page area has the given size.", advanced: true,
			schema: object(with(props{"width": prop("number", "Page width in CSS pixels"), "height": prop("number", "Page height in CSS pixels")}, "pageId"), "width", "height"),
			run:    resizePage},
	}
}

func navigatePage(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		pageArg
		Type               string `json:"type"`
		URL                string `json:"url"`
		IgnoreCache        bool   `json:"ignoreCache"`
		HandleBeforeUnload string `json:"handleBeforeUnload"`
		InitScript         string `json:"initScript"`
		Timeout            int    `json:"timeout"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if a.Type == "" {
		a.Type = "url"
	}
	if a.Type == "url" && a.URL == "" {
		return nil, toolError("url is required to navigate; or set type to back, forward or reload")
	}
	p, err := c.page(ctx, a.PageID)
	if err != nil {
		return nil, err
	}
	limit := waitLimit(a.Timeout)
	unload := a.HandleBeforeUnload
	if unload != "dismiss" {
		unload = "accept"
	}
	p.mu.Lock()
	p.autoDialog, p.unloadOnly = unload, true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.autoDialog, p.unloadOnly = "", false
		p.mu.Unlock()
	}()
	if a.InitScript != "" {
		var added struct {
			Identifier string `json:"identifier"`
		}
		res, err := p.call(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": a.InitScript})
		if err = into(res, err, &added); err != nil {
			return nil, err
		}
		defer func() {
			clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = p.call(clean, "Page.removeScriptToEvaluateOnNewDocument", map[string]any{"identifier": added.Identifier})
		}()
	}
	var state string
	switch a.Type {
	case "url":
		if state, err = p.goTo(ctx, a.URL, limit); err != nil {
			return nil, err
		}
	case "reload":
		if _, err = p.call(ctx, "Page.reload", map[string]any{"ignoreCache": a.IgnoreCache}); err != nil {
			return nil, err
		}
		pause(ctx, 150*time.Millisecond)
		state = p.loaded(ctx, limit)
	case "back", "forward":
		var history struct {
			CurrentIndex int `json:"currentIndex"`
			Entries      []struct {
				ID int `json:"id"`
			} `json:"entries"`
		}
		res, err := p.call(ctx, "Page.getNavigationHistory", nil)
		if err = into(res, err, &history); err != nil {
			return nil, err
		}
		index := history.CurrentIndex - 1
		if a.Type == "forward" {
			index = history.CurrentIndex + 1
		}
		if index < 0 || index >= len(history.Entries) {
			return nil, toolError("The page has no history to go %s to", a.Type)
		}
		if _, err = p.call(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": history.Entries[index].ID}); err != nil {
			return nil, err
		}
		pause(ctx, 150*time.Millisecond)
		state = p.loaded(ctx, limit)
	default:
		return nil, toolError("type must be url, back, forward or reload")
	}
	return p.finish(ctx, state, false)
}

func takeScreenshot(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		pageArg
		Format   string   `json:"format"`
		Quality  *float64 `json:"quality"`
		UID      string   `json:"uid"`
		FullPage bool     `json:"fullPage"`
		FilePath string   `json:"filePath"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if a.Format == "" {
		a.Format = "png"
	}
	if a.Format != "png" && a.Format != "jpeg" && a.Format != "webp" {
		return nil, toolError("format must be png, jpeg or webp")
	}
	if a.UID != "" && a.FullPage {
		return nil, toolError("Pass uid or fullPage, not both")
	}
	if a.FilePath != "" {
		if err := c.checkPath(a.FilePath); err != nil {
			return nil, err
		}
	}
	p, err := c.page(ctx, a.PageID)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"format": a.Format}
	if a.Quality != nil && a.Format != "png" {
		params["quality"] = int(*a.Quality)
	}
	what := "the visible part of the page"
	var metrics struct {
		View struct {
			PageX float64 `json:"pageX"`
			PageY float64 `json:"pageY"`
		} `json:"cssVisualViewport"`
		Content struct {
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		} `json:"cssContentSize"`
	}
	switch {
	case a.UID != "":
		node, err := p.node(a.UID)
		if err != nil {
			return nil, err
		}
		_, _ = p.call(ctx, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": node})
		var box struct {
			Model struct {
				Border []float64 `json:"border"`
			} `json:"model"`
		}
		res, err := p.call(ctx, "DOM.getBoxModel", map[string]any{"backendNodeId": node})
		if err = into(res, err, &box); err != nil || len(box.Model.Border) != 8 {
			return nil, toolError("The element has no visible box to capture")
		}
		res, err = p.call(ctx, "Page.getLayoutMetrics", nil)
		if err = into(res, err, &metrics); err != nil {
			return nil, err
		}
		b := box.Model.Border
		x1, y1 := min(b[0], b[2], b[4], b[6]), min(b[1], b[3], b[5], b[7])
		x2, y2 := max(b[0], b[2], b[4], b[6]), max(b[1], b[3], b[5], b[7])
		params["clip"] = map[string]any{"x": x1 + metrics.View.PageX, "y": y1 + metrics.View.PageY, "width": x2 - x1, "height": y2 - y1, "scale": 1}
		params["captureBeyondViewport"] = true
		what = "the element"
	case a.FullPage:
		res, err := p.call(ctx, "Page.getLayoutMetrics", nil)
		if err = into(res, err, &metrics); err != nil {
			return nil, err
		}
		params["clip"] = map[string]any{"x": 0, "y": 0, "width": metrics.Content.Width, "height": metrics.Content.Height, "scale": 1}
		params["captureBeyondViewport"] = true
		what = "the full page"
	}
	var shot struct {
		Data string `json:"data"`
	}
	capture := func() error {
		res, err := p.call(ctx, "Page.captureScreenshot", params)
		return into(res, err, &shot)
	}
	if err = capture(); err != nil {
		return nil, err
	}
	if a.FilePath != "" {
		data, err := base64.StdEncoding.DecodeString(shot.Data)
		if err != nil {
			return nil, err
		}
		if err = c.save(a.FilePath, data); err != nil {
			return nil, err
		}
		return textResult(fmt.Sprintf("Saved a screenshot of %s to %s", what, a.FilePath)), nil
	}
	note := ""
	if a.Format == "png" && len(shot.Data) > 4<<20 {
		// A large PNG costs the reader far more than it shows.
		a.Format, params["format"], params["quality"] = "jpeg", "jpeg", 80
		if err = capture(); err != nil {
			return nil, err
		}
		note = " It was large, so it is a JPEG; pass filePath to keep a PNG."
	}
	return map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "Took a screenshot of " + what + "." + note},
		map[string]any{"type": "image", "data": shot.Data, "mimeType": "image/" + a.Format},
	}}, nil
}

func uploadFile(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		elementArgs
		FilePaths []string `json:"filePaths"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if len(a.FilePaths) == 0 {
		return nil, toolError("filePaths must list at least one file")
	}
	for _, path := range a.FilePaths {
		if err := c.checkPath(path); err != nil {
			return nil, err
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return nil, toolError("%s is not a file", path)
		}
	}
	p, err := c.page(ctx, a.PageID)
	if err != nil {
		return nil, err
	}
	node, err := p.node(a.UID)
	if err != nil {
		return nil, err
	}
	isInput, err := p.callOn(ctx, node, `function() { const el = this.nodeType === 1 ? this : this.parentElement; return !!el && el.tagName === 'INPUT' && el.type === 'file'; }`)
	if err != nil {
		return nil, err
	}
	if string(isInput) != "true" {
		// The element opens the file chooser: catch it instead of showing it.
		select {
		case <-p.chooser:
		default:
		}
		if _, err = p.call(ctx, "Page.setInterceptFileChooserDialog", map[string]any{"enabled": true}); err != nil {
			return nil, err
		}
		defer func() {
			clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = p.call(clean, "Page.setInterceptFileChooserDialog", map[string]any{"enabled": false})
		}()
		if err = p.clickNode(ctx, node, false); err != nil {
			return nil, err
		}
		select {
		case node = <-p.chooser:
		case <-time.After(5 * time.Second):
			return nil, toolError("Clicking the element did not open a file chooser. Use the uid of the file input or of the button that opens the chooser")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if _, err = p.call(ctx, "DOM.setFileInputFiles", map[string]any{"files": a.FilePaths, "backendNodeId": node}); err != nil {
		return nil, err
	}
	p.settle(ctx, 10*time.Second)
	return p.finish(ctx, fmt.Sprintf("Chose %d file(s).", len(a.FilePaths)), a.IncludeSnapshot)
}

func waitForText(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		pageArg
		Text    []string `json:"text"`
		Timeout int      `json:"timeout"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if len(a.Text) == 0 {
		return nil, toolError("text must list at least one text")
	}
	p, err := c.page(ctx, a.PageID)
	if err != nil {
		return nil, err
	}
	wanted, _ := json.Marshal(a.Text)
	expression := `(texts => { const t = document.body ? document.body.innerText : ''; const found = texts.find(x => t.includes(x)); return found === undefined ? null : found; })(` + string(wanted) + `)`
	limit := waitLimit(a.Timeout)
	deadline := time.Now().Add(limit)
	for {
		p.mu.Lock()
		blocked := p.dialog != nil
		p.mu.Unlock()
		if blocked {
			return p.finish(ctx, "Stopped waiting: a dialog is open.", false)
		}
		poll, cancel := context.WithTimeout(ctx, 5*time.Second)
		res, err := p.eval(poll, expression)
		cancel()
		var found *string
		if err == nil && json.Unmarshal(res, &found) == nil && found != nil {
			return p.finish(ctx, fmt.Sprintf("Found %q on the page.", *found), true)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !p.c.conn.alive() {
			return nil, errChromeClosed
		}
		if time.Now().After(deadline) {
			return nil, toolError("None of the texts appeared within %s", limit)
		}
		pause(ctx, 250*time.Millisecond)
	}
}

func evaluateScript(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		pageArg
		Function         string   `json:"function"`
		Args             []string `json:"args"`
		FilePath         string   `json:"filePath"`
		DialogAction     string   `json:"dialogAction"`
		WaitForStableDom *bool    `json:"waitForStableDom"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Function) == "" {
		return nil, toolError("function is required")
	}
	if a.FilePath != "" {
		if err := c.checkPath(a.FilePath); err != nil {
			return nil, err
		}
	}
	p, err := c.page(ctx, a.PageID)
	if err != nil {
		return nil, err
	}
	arguments := make([]map[string]any, len(a.Args))
	for i, uid := range a.Args {
		node, err := p.node(uid)
		if err != nil {
			return nil, err
		}
		object, err := p.object(ctx, node)
		if err != nil {
			return nil, err
		}
		arguments[i] = map[string]any{"objectId": object}
	}
	var global evalReply
	res, err := p.call(ctx, "Runtime.evaluate", map[string]any{"expression": "globalThis"})
	if err = into(res, err, &global); err != nil {
		return nil, err
	}
	if a.DialogAction == "" {
		a.DialogAction = "accept"
	}
	p.mu.Lock()
	p.autoDialog = a.DialogAction
	p.mu.Unlock()
	var reply evalReply
	res, err = p.call(ctx, "Runtime.callFunctionOn", map[string]any{
		"objectId": global.Result.ObjectID, "functionDeclaration": "async function(...args) { return await (" + a.Function + "\n)(...args); }",
		"arguments": arguments, "returnByValue": true, "awaitPromise": true, "userGesture": true,
	})
	p.mu.Lock()
	p.autoDialog = ""
	p.mu.Unlock()
	if err = into(res, err, &reply); err != nil {
		return nil, toolError("The script could not run or its result is not JSON-serializable: %v", err)
	}
	if reply.ExceptionDetails != nil {
		return nil, toolError("The script threw: %s", reply.ExceptionDetails.message())
	}
	value := string(reply.Result.Value)
	if value == "" {
		value = "undefined"
		if reply.Result.UnserializableValue != "" {
			value = reply.Result.UnserializableValue
		}
	}
	if a.WaitForStableDom == nil || *a.WaitForStableDom {
		p.settle(ctx, 10*time.Second)
	}
	if a.FilePath != "" {
		if err = c.save(a.FilePath, []byte(value)); err != nil {
			return nil, err
		}
		return p.finish(ctx, "Saved the script's result to "+a.FilePath, false)
	}
	return p.finish(ctx, "The script returned:\n```json\n"+value+"\n```", false)
}

func listConsole(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		pageArg
		PageSize  int      `json:"pageSize"`
		PageIdx   int      `json:"pageIdx"`
		Types     []string `json:"types"`
		Preserved bool     `json:"includePreservedMessages"`
		Stacks    bool     `json:"includeStackTraces"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	p, err := c.page(ctx, a.PageID)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, kind := range a.Types {
		wanted[kind] = true
	}
	p.mu.Lock()
	var found []consoleMsg
	for _, m := range p.console {
		if (a.Preserved || m.gen == p.gen) && (len(wanted) == 0 || wanted[m.kind]) {
			found = append(found, m)
		}
	}
	p.mu.Unlock()
	from, to, note := paginate(len(found), a.PageSize, a.PageIdx)
	var sb strings.Builder
	sb.WriteString("## Console messages\n" + note)
	for _, m := range found[from:to] {
		text := m.text
		if len(text) > 1000 {
			text = strings.ToValidUTF8(text[:1000], "") + "… (chrome_get_console_message has the rest)"
		}
		fmt.Fprintf(&sb, "msgid=%d [%s] %s\n", m.id, m.kind, text)
		if a.Stacks && m.stack != "" {
			sb.WriteString(m.stack)
		}
	}
	if len(found) == 0 {
		sb.WriteString("(none)\n")
	}
	return textResult(sb.String()), nil
}

func listNetwork(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		pageArg
		PageSize  int      `json:"pageSize"`
		PageIdx   int      `json:"pageIdx"`
		Types     []string `json:"resourceTypes"`
		Preserved bool     `json:"includePreservedRequests"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	p, err := c.page(ctx, a.PageID)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, kind := range a.Types {
		wanted[strings.ToLower(kind)] = true
	}
	var lines []string
	p.mu.Lock()
	for _, r := range p.requests {
		if (a.Preserved || r.gen == p.gen) && (len(wanted) == 0 || wanted[r.kind]) {
			url := r.url
			if len(url) > 300 {
				url = strings.ToValidUTF8(url[:300], "") + "…"
			}
			lines = append(lines, fmt.Sprintf("reqid=%d %s %s [%s]\n", r.id, r.method, url, r.state()))
		}
	}
	p.mu.Unlock()
	from, to, note := paginate(len(lines), a.PageSize, a.PageIdx)
	text := "## Network requests\n" + note + strings.Join(lines[from:to], "")
	if len(lines) == 0 {
		text += "(none)\n"
	}
	return textResult(text), nil
}

func getNetwork(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		pageArg
		ReqID        *int   `json:"reqid"`
		RequestPath  string `json:"requestFilePath"`
		ResponsePath string `json:"responseFilePath"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if a.ReqID == nil {
		return nil, toolError("reqid is required; chrome_list_network_requests lists them")
	}
	for _, path := range []string{a.RequestPath, a.ResponsePath} {
		if path != "" {
			if err := c.checkPath(path); err != nil {
				return nil, err
			}
		}
	}
	p, err := c.page(ctx, a.PageID)
	if err != nil {
		return nil, err
	}
	var r netRequest
	p.mu.Lock()
	for _, candidate := range p.requests {
		if candidate.id == *a.ReqID {
			r = *candidate
		}
	}
	p.mu.Unlock()
	if r.id == 0 {
		return nil, toolError("No network request with reqid %d", *a.ReqID)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Request %s\nMethod: %s\nStatus: %s", r.url, r.method, r.state())
	if r.statusText != "" {
		sb.WriteString(" " + r.statusText)
	}
	sb.WriteString("\n### Request headers\n")
	headerLines(&sb, r.headers)
	body := func(title, path string, data []byte, missing string) error {
		switch {
		case data == nil:
			if missing != "" {
				fmt.Fprintf(&sb, "### %s\n(%s)\n", title, missing)
			}
		case path != "":
			if err := c.save(path, data); err != nil {
				return err
			}
			fmt.Fprintf(&sb, "### %s\nSaved to %s (%d bytes)\n", title, path, len(data))
		default:
			text := strings.ToValidUTF8(string(data[:min(len(data), inlineBody)]), "�")
			fmt.Fprintf(&sb, "### %s\n%s\n", title, text)
			if len(data) > inlineBody {
				fmt.Fprintf(&sb, "… (%d bytes in all; pass a file path to save the whole body)\n", len(data))
			}
		}
		return nil
	}
	var posted []byte
	if r.postData != "" {
		posted = []byte(r.postData)
	} else if r.hasPost {
		var reply struct {
			PostData string `json:"postData"`
		}
		res, err := p.call(ctx, "Network.getRequestPostData", map[string]any{"requestId": r.requestID})
		if into(res, err, &reply) == nil {
			posted = []byte(reply.PostData)
		}
	}
	if err = body("Request body", a.RequestPath, posted, ""); err != nil {
		return nil, err
	}
	if r.responded {
		sb.WriteString("### Response headers\n")
		headerLines(&sb, r.respHeaders)
		var reply struct {
			Body          string `json:"body"`
			Base64Encoded bool   `json:"base64Encoded"`
		}
		var received []byte
		res, err := p.call(ctx, "Network.getResponseBody", map[string]any{"requestId": r.requestID})
		if into(res, err, &reply) == nil {
			received = []byte(reply.Body)
			if reply.Base64Encoded {
				received, _ = base64.StdEncoding.DecodeString(reply.Body)
				if a.ResponsePath == "" {
					received = []byte(fmt.Sprintf("(binary, %d bytes, %s; pass responseFilePath to save it)", len(received), r.mime))
				}
			}
		}
		if err = body("Response body", a.ResponsePath, received, "Chrome no longer has it"); err != nil {
			return nil, err
		}
	}
	return textResult(sb.String()), nil
}

func emulate(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		pageArg
		Network     *string  `json:"networkConditions"`
		CPU         *float64 `json:"cpuThrottlingRate"`
		Geolocation *string  `json:"geolocation"`
		UserAgent   *string  `json:"userAgent"`
		ColorScheme *string  `json:"colorScheme"`
		Viewport    *string  `json:"viewport"`
		Headers     *string  `json:"extraHttpHeaders"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	p, err := c.page(ctx, a.PageID)
	if err != nil {
		return nil, err
	}
	var done []string
	set := func(what, method string, params any) error {
		if _, err := p.call(ctx, method, params); err != nil {
			return err
		}
		if what != "" {
			done = append(done, what)
		}
		return nil
	}
	if a.Network != nil {
		preset := networkPresets[*a.Network]
		if preset == nil {
			return nil, toolError("networkConditions must be No emulation, Offline, Slow 3G, Fast 3G, Slow 4G or Fast 4G")
		}
		if err = set("network: "+*a.Network, "Network.emulateNetworkConditions", preset); err != nil {
			return nil, err
		}
	}
	if a.CPU != nil {
		if *a.CPU < 1 || *a.CPU > 20 {
			return nil, toolError("cpuThrottlingRate must be between 1 and 20")
		}
		if err = set(fmt.Sprintf("CPU slowdown: %gx", *a.CPU), "Emulation.setCPUThrottlingRate", map[string]any{"rate": *a.CPU}); err != nil {
			return nil, err
		}
	}
	if a.Geolocation != nil {
		if *a.Geolocation == "" {
			err = set("geolocation cleared", "Emulation.clearGeolocationOverride", nil)
		} else {
			var lat, lon float64
			latText, lonText, ok := strings.Cut(*a.Geolocation, ",")
			lat, errLat := strconv.ParseFloat(strings.TrimSpace(latText), 64)
			lon, errLon := strconv.ParseFloat(strings.TrimSpace(lonText), 64)
			if !ok || errLat != nil || errLon != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
				return nil, toolError("geolocation must be \"<latitude>,<longitude>\"")
			}
			err = set("geolocation: "+*a.Geolocation, "Emulation.setGeolocationOverride", map[string]any{"latitude": lat, "longitude": lon, "accuracy": 1})
		}
		if err != nil {
			return nil, err
		}
	}
	if a.UserAgent != nil {
		what := "user agent: " + *a.UserAgent
		if *a.UserAgent == "" {
			what = "user agent cleared"
		}
		if err = set(what, "Emulation.setUserAgentOverride", map[string]any{"userAgent": *a.UserAgent}); err != nil {
			return nil, err
		}
	}
	if a.ColorScheme != nil {
		value := *a.ColorScheme
		if value == "auto" {
			value = ""
		}
		if err = set("color scheme: "+*a.ColorScheme, "Emulation.setEmulatedMedia", map[string]any{"features": []map[string]any{{"name": "prefers-color-scheme", "value": value}}}); err != nil {
			return nil, err
		}
	}
	if a.Viewport != nil {
		if *a.Viewport == "" {
			if err = set("viewport cleared", "Emulation.clearDeviceMetricsOverride", nil); err == nil {
				err = set("", "Emulation.setTouchEmulationEnabled", map[string]any{"enabled": false})
			}
		} else {
			size, flags, _ := strings.Cut(*a.Viewport, ",")
			dims := strings.Split(size, "x")
			var width, height int
			ratio := 1.0
			var errW, errH, errR error
			if len(dims) >= 2 {
				width, errW = strconv.Atoi(strings.TrimSpace(dims[0]))
				height, errH = strconv.Atoi(strings.TrimSpace(dims[1]))
			}
			if len(dims) == 3 {
				ratio, errR = strconv.ParseFloat(strings.TrimSpace(dims[2]), 64)
			}
			if len(dims) < 2 || len(dims) > 3 || errW != nil || errH != nil || errR != nil || width <= 0 || height <= 0 || ratio <= 0 {
				return nil, toolError("viewport must be \"<width>x<height>x<devicePixelRatio>[,mobile][,touch][,landscape]\"")
			}
			metrics := map[string]any{"width": width, "height": height, "deviceScaleFactor": ratio, "mobile": false}
			touch := false
			for _, flag := range strings.Split(flags, ",") {
				switch strings.TrimSpace(flag) {
				case "mobile":
					metrics["mobile"] = true
				case "touch":
					touch = true
				case "landscape":
					metrics["screenOrientation"] = map[string]any{"type": "landscapePrimary", "angle": 90}
				}
			}
			if err = set("viewport: "+*a.Viewport, "Emulation.setDeviceMetricsOverride", metrics); err == nil {
				err = set("", "Emulation.setTouchEmulationEnabled", map[string]any{"enabled": touch})
			}
		}
		if err != nil {
			return nil, err
		}
	}
	if a.Headers != nil {
		headers := map[string]string{}
		if *a.Headers != "" && json.Unmarshal([]byte(*a.Headers), &headers) != nil {
			return nil, toolError("extraHttpHeaders must be a JSON object of strings")
		}
		if err = set(fmt.Sprintf("%d extra HTTP header(s)", len(headers)), "Network.setExtraHTTPHeaders", map[string]any{"headers": headers}); err != nil {
			return nil, err
		}
	}
	if len(done) == 0 {
		return nil, toolError("Pass at least one property to emulate")
	}
	return textResult("Emulation set on this page: " + strings.Join(done, "; ") + "."), nil
}

func resizePage(ctx context.Context, c *chrome, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		pageArg
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if a.Width < 1 || a.Height < 1 {
		return nil, toolError("width and height must be positive")
	}
	p, err := c.page(ctx, a.PageID)
	if err != nil {
		return nil, err
	}
	var window struct {
		WindowID int `json:"windowId"`
		Bounds   struct {
			Width       float64 `json:"width"`
			Height      float64 `json:"height"`
			WindowState string  `json:"windowState"`
		} `json:"bounds"`
	}
	get := func() error {
		res, err := c.conn.call(ctx, "", "Browser.getWindowForTarget", map[string]any{"targetId": p.target})
		return into(res, err, &window)
	}
	if err = get(); err != nil {
		return nil, err
	}
	if window.Bounds.WindowState != "normal" && window.Bounds.WindowState != "" {
		if _, err = c.conn.call(ctx, "", "Browser.setWindowBounds", map[string]any{"windowId": window.WindowID, "bounds": map[string]any{"windowState": "normal"}}); err != nil {
			return nil, err
		}
		pause(ctx, 300*time.Millisecond)
		if err = get(); err != nil {
			return nil, err
		}
	}
	var inner [2]float64
	res, err := p.eval(ctx, `[innerWidth, innerHeight]`)
	if err != nil || json.Unmarshal(res, &inner) != nil {
		return nil, toolError("Could not read the page's size")
	}
	bounds := map[string]any{"width": int(window.Bounds.Width + a.Width - inner[0]), "height": int(window.Bounds.Height + a.Height - inner[1])}
	if _, err = c.conn.call(ctx, "", "Browser.setWindowBounds", map[string]any{"windowId": window.WindowID, "bounds": bounds}); err != nil {
		return nil, err
	}
	pause(ctx, 300*time.Millisecond)
	if res, err = p.eval(ctx, `[innerWidth, innerHeight]`); err == nil {
		_ = json.Unmarshal(res, &inner)
	}
	return textResult(fmt.Sprintf("The page area is now %gx%g.", inner[0], inner[1])), nil
}
