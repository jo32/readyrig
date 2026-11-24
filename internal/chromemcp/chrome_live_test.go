package chromemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const livePage = `<!doctype html><html><head><title>Form page</title></head><body>
<h1>Sign up</h1>
<form onsubmit="event.preventDefault(); document.getElementById('out').textContent = 'Saved ' + name.value + ' / ' + plan.value + ' / ' + news.checked + ' / ' + when.value">
<label>Name <input id="name" name="name" value="old"></label>
<label>Plan <select id="plan"><option value="f">Free</option><option value="p">Pro plan</option></select></label>
<label><input type="checkbox" id="news"> Newsletter</label>
<label>When <input type="date" id="when"></label>
<label>Notes <textarea id="notes"></textarea></label>
<label>Avatar <input type="file" id="file" onchange="document.getElementById('picked').textContent = 'Picked ' + this.files[0].name"></label>
<button type="submit">Save</button>
</form>
<p id="out"></p><p id="picked"></p>
<button onclick="alert('Hello dialog'); document.getElementById('out').textContent = 'After alert'">Warn</button>
<button onclick="setTimeout(() => document.getElementById('out').textContent = 'Late text', 600)">Later</button>
<button id="dbl" ondblclick="this.textContent = 'Doubled'">Twice</button>
<div id="keys" tabindex="0" onkeydown="this.textContent = 'Key ' + (event.ctrlKey ? 'ctrl+' : '') + (event.shiftKey ? 'shift+' : '') + event.key">Keys here</div>
<a href="/second">Second page</a>
<iframe src="/frame" title="Inner"></iframe>
<script>console.log('page ready', {a: 1}); console.error('bad thing'); fetch('/api').then(r => r.text())</script>
</body></html>`

// TestChromeToolsAgainstRealChrome drives a private headless Chrome through the
// tools the way an agent does. It is skipped in CI and where Chrome is not installed.
func TestChromeToolsAgainstRealChrome(t *testing.T) {
	// The release workflow runs the tests as its gate; a hosted runner's Chrome
	// and timing are not what this test was written against.
	if testing.Short() || os.Getenv("CI") != "" {
		t.Skip("needs a real Chrome on a developer machine")
	}
	bins, _ := chromePaths()
	binary := ""
	for _, candidate := range bins {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			binary = candidate
			break
		}
	}
	if binary == "" {
		t.Skip("Google Chrome is not installed")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	profile := t.TempDir()
	cmd := exec.Command(binary, "--headless=new", fmt.Sprintf("--remote-debugging-port=%d", port), "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "--disable-gpu", "--window-size=1000,800", "about:blank")
	if err = cmd.Start(); err != nil {
		t.Skip("Chrome did not start:", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	mux := http.NewServeMux()
	html := func(path, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		})
	}
	html("/", livePage)
	html("/second", `<!doctype html><title>Second</title><h1>Second page</h1><a href="/">Home</a>`)
	html("/frame", `<!doctype html><button>Inside frame</button>`)
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	})
	site := httptest.NewServer(mux)
	t.Cleanup(site.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var found target
	for deadline := time.Now().Add(20 * time.Second); ; {
		if found, err = probeURL(ctx, fmt.Sprintf("http://127.0.0.1:%d", port)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Skip("Chrome's debugging port did not open:", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	root := t.TempDir()
	br := newChrome(nil, func() []string { return []string{root} })
	conn, err := dialCDP(ctx, found.Endpoint, br.event)
	if err != nil {
		t.Fatal(err)
	}
	br.conn = conn
	t.Cleanup(conn.Close)

	tools := map[string]chromeTool{}
	for _, tool := range chromeCatalog() {
		tools[tool.name] = tool
	}
	verbose := os.Getenv("READYRIG_CHROME_TEST_LOG") != ""
	call := func(name string, args map[string]any) (string, error) {
		t.Helper()
		raw, _ := json.Marshal(args)
		result, err := tools[name].run(ctx, br, raw)
		if err != nil {
			if verbose {
				t.Logf("%s %s -> ERROR %v", name, raw, err)
			}
			return "", err
		}
		text := resultText(result)
		if verbose {
			t.Logf("%s %s ->\n%s", name, raw, text)
		}
		return text, nil
	}
	must := func(name string, args map[string]any, want ...string) string {
		t.Helper()
		text, err := call(name, args)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, w := range want {
			if !strings.Contains(text, w) {
				t.Fatalf("%s: %q is missing from:\n%s", name, w, text)
			}
		}
		return text
	}
	// uid finds the uid of the first snapshot line that contains every given part.
	uid := func(snapshot string, parts ...string) string {
		t.Helper()
	lines:
		for _, line := range strings.Split(snapshot, "\n") {
			for _, part := range parts {
				if !strings.Contains(line, part) {
					continue lines
				}
			}
			return strings.TrimPrefix(strings.Fields(line)[0], "uid=")
		}
		t.Fatalf("no snapshot line with %q in:\n%s", parts, snapshot)
		return ""
	}

	must("list_pages", nil, "## Pages", "1: about:blank")
	if _, err = call("take_snapshot", nil); err == nil {
		t.Fatal("acted on a page although none was selected")
	}
	must("new_page", map[string]any{"url": site.URL}, "Opened page 2", "Form page")
	snap := must("take_snapshot", nil, `RootWebArea "Form page"`, `heading "Sign up" level="1"`, `textbox "Name" value="old"`, `button "Inside frame"`, `link "Second page"`)

	// Fill every kind of field in one call, then submit.
	must("fill_form", map[string]any{"elements": []map[string]any{
		{"uid": uid(snap, "textbox", `"Name"`), "value": "Ada L"},
		{"uid": uid(snap, "combobox"), "value": "Pro plan"},
		{"uid": uid(snap, "checkbox"), "value": "true"},
		{"uid": uid(snap, `"When"`), "value": "2026-03-04"},
		{"uid": uid(snap, "textbox", `"Notes"`), "value": "line one"},
	}}, "Filled 5 elements")
	snap = must("click", map[string]any{"uid": uid(snap, "button", `"Save"`), "includeSnapshot": true}, "Saved Ada L / p / true / 2026-03-04", `value="line one"`)

	// An empty value clears a field; typing goes to the focused field.
	must("fill", map[string]any{"uid": uid(snap, "textbox", `"Name"`), "value": ""})
	must("type_text", map[string]any{"text": "Bo", "submitKey": "Enter"})
	snap = must("take_snapshot", nil, "Saved Bo / p / true")

	// Keys, double click and hover.
	must("click", map[string]any{"uid": uid(snap, "Keys here")})
	must("press_key", map[string]any{"key": "Control+Shift+K", "includeSnapshot": true}, "Key ctrl+shift+K")
	must("press_key", map[string]any{"key": "ArrowDown", "includeSnapshot": true}, "Key ArrowDown")
	if _, err = call("press_key", map[string]any{"key": "Hyper+X"}); err == nil {
		t.Fatal("accepted an unknown modifier")
	}
	snap = must("click", map[string]any{"uid": uid(snap, "button", `"Twice"`), "dblClick": true, "includeSnapshot": true}, `button "Doubled"`)
	must("hover", map[string]any{"uid": uid(snap, "button", `"Save"`)}, "Hovering")

	// A dialog blocks the page until it is answered.
	must("click", map[string]any{"uid": uid(snap, "button", `"Warn"`)}, "## Open dialog", "alert: Hello dialog")
	must("handle_dialog", map[string]any{"action": "accept"}, "Answered the dialog")
	snap = must("take_snapshot", nil, "After alert")
	if _, err = call("handle_dialog", map[string]any{"action": "accept"}); err == nil {
		t.Fatal("answered a dialog that is not open")
	}

	// Waiting for text that appears later.
	must("click", map[string]any{"uid": uid(snap, "button", `"Later"`)})
	snap = must("wait_for", map[string]any{"text": []string{"Nope", "Late text"}, "timeout": 5000}, `Found "Late text"`, "## Latest page snapshot")
	if _, err = call("wait_for", map[string]any{"text": []string{"Never there"}, "timeout": 300}); err == nil {
		t.Fatal("found text that is not on the page")
	}

	// Scripts, with and without element arguments.
	must("evaluate_script", map[string]any{"function": "() => ({title: document.title, n: 1 + 1})"}, `{"title":"Form page","n":2}`)
	must("evaluate_script", map[string]any{"function": "(el) => el.tagName", "args": []string{uid(snap, "button", `"Save"`)}}, `"BUTTON"`)
	must("evaluate_script", map[string]any{"function": "async () => { await new Promise(r => setTimeout(r, 50)); return confirm('sure?') }"}, "true")
	must("evaluate_script", map[string]any{"function": "() => confirm('sure?')", "dialogAction": "dismiss"}, "false")
	if _, err = call("evaluate_script", map[string]any{"function": "() => { throw new Error('boom') }"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a throwing script reported: %v", err)
	}

	// Files: uploads and saved output stay inside the approved folders.
	upload := filepath.Join(root, "avatar.txt")
	if err = os.WriteFile(upload, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	must("upload_file", map[string]any{"uid": uid(snap, `"Avatar"`), "filePaths": []string{upload}, "includeSnapshot": true}, "Picked avatar.txt")
	outside := filepath.Join(filepath.Dir(profile), "outside.png")
	if home, herr := os.UserHomeDir(); herr == nil {
		outside = filepath.Join(home, "readyrig-outside-test.png")
	}
	if _, err = call("take_screenshot", map[string]any{"filePath": outside}); err == nil {
		os.Remove(outside)
		t.Fatal("saved a screenshot outside the approved folders")
	}
	if _, err = call("upload_file", map[string]any{"uid": uid(snap, `"Avatar"`), "filePaths": []string{"/etc/hosts"}}); err == nil {
		t.Fatal("uploaded a file from outside the approved folders")
	}
	shot := filepath.Join(root, "shot.png")
	must("take_screenshot", map[string]any{"filePath": shot, "fullPage": true}, "Saved a screenshot of the full page")
	if data, _ := os.ReadFile(shot); len(data) < 1000 || string(data[1:4]) != "PNG" {
		t.Fatalf("the saved screenshot is not a PNG (%d bytes)", len(data))
	}
	raw, _ := json.Marshal(map[string]any{"uid": uid(snap, "button", `"Save"`), "format": "jpeg", "quality": 60})
	inline, err := tools["take_screenshot"].run(ctx, br, raw)
	if err != nil {
		t.Fatal(err)
	}
	if image, _ := inline["content"].([]any)[1].(map[string]any); image["type"] != "image" || image["mimeType"] != "image/jpeg" || len(image["data"].(string)) < 200 {
		t.Fatalf("no inline element screenshot: %v", inline)
	}

	// Console and network are recorded from the first use of the page.
	must("navigate_page", map[string]any{"type": "reload"}, "Form page")
	must("list_console_messages", nil, "[log] page ready {a: 1}", "[error] bad thing")
	must("list_console_messages", map[string]any{"types": []string{"error"}}, "bad thing")
	network := must("list_network_requests", nil, "GET "+site.URL+"/ [200]", "/api [200]", "/frame [200]")
	api := ""
	for _, line := range strings.Split(network, "\n") {
		if strings.Contains(line, "/api") {
			api = strings.TrimPrefix(strings.Fields(line)[0], "reqid=")
		}
	}
	var reqid int
	fmt.Sscan(api, &reqid)
	must("get_network_request", map[string]any{"reqid": reqid}, "### Response headers", "Content-Type: application/json", `{"ok":true}`)
	if only := must("list_network_requests", map[string]any{"resourceTypes": []string{"fetch"}}); strings.Contains(only, "/frame") || !strings.Contains(only, "/api") {
		t.Fatalf("resource type filter failed:\n%s", only)
	}

	// Navigation by link, history and URL; old uids stop working.
	snap = must("take_snapshot", nil)
	link := uid(snap, "link", `"Second page"`)
	must("click", map[string]any{"uid": link, "includeSnapshot": true}, `heading "Second page"`)
	if _, err = call("click", map[string]any{"uid": link}); err == nil {
		t.Fatal("used a uid from before the navigation")
	}
	must("navigate_page", map[string]any{"type": "back"}, "Form page")
	must("navigate_page", map[string]any{"type": "forward"}, "Second")
	must("navigate_page", map[string]any{"url": site.URL + "/second", "initScript": "window.injected = 7"}, "Second")
	must("evaluate_script", map[string]any{"function": "() => window.injected"}, "7")
	if _, err = call("navigate_page", map[string]any{"url": "http://127.0.0.1:1/"}); err == nil {
		t.Fatal("reported success for a page that cannot load")
	}

	// Emulation and window size.
	must("emulate", map[string]any{"viewport": "400x700x2,mobile,touch", "colorScheme": "dark", "userAgent": "RigTest/1"}, "viewport: 400x700x2")
	must("evaluate_script", map[string]any{"function": "() => [innerWidth, devicePixelRatio, matchMedia('(prefers-color-scheme: dark)').matches, navigator.userAgent]"}, `[400,2,true,"RigTest/1"]`)
	must("emulate", map[string]any{"viewport": "", "colorScheme": "auto", "userAgent": ""})
	must("resize_page", map[string]any{"width": 900, "height": 600}, "900x600")

	// Pages: select, close, and the last one stays.
	must("select_page", map[string]any{"pageId": 1}, "1: about:blank [selected]")
	must("close_page", map[string]any{"pageId": 2}, "Closed page 2")
	if _, err = call("close_page", map[string]any{"pageId": 1}); err == nil {
		t.Fatal("closed the last page")
	}
	if _, err = call("take_snapshot", map[string]any{"pageId": 2}); err == nil {
		t.Fatal("used a closed page")
	}
}
