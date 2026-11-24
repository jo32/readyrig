package chromemcp

import (
	"computer-use-server/internal/harness"
	"computer-use-server/internal/store"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeChrome is a debugging endpoint that answers the few browser-level
// commands the bridge needs, and counts connections: each one stands for a
// permission dialog in the real Chrome.
type fakeChrome struct {
	server *httptest.Server
	dials  atomic.Int32
	mu     sync.Mutex
	conns  []*websocket.Conn
}

func newFakeChrome(t *testing.T) *fakeChrome {
	t.Helper()
	f := &fakeChrome{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		f.dials.Add(1)
		f.mu.Lock()
		f.conns = append(f.conns, conn)
		f.mu.Unlock()
		for {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var msg struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(data, &msg)
			var result any = map[string]any{}
			if msg.Method == "Target.getTargets" {
				result = map[string]any{"targetInfos": []any{
					map[string]any{"targetId": "T1", "type": "page", "title": "Example", "url": "https://example.com/"},
					map[string]any{"targetId": "T2", "type": "service_worker", "url": "https://example.com/sw.js"},
					map[string]any{"targetId": "T3", "type": "page", "url": "devtools://devtools/bundled/inspector.html"},
				}}
			}
			// An event between replies must not be taken for one.
			_ = conn.Write(context.Background(), websocket.MessageText, []byte(`{"method":"Target.targetInfoChanged","params":{}}`))
			reply, _ := json.Marshal(map[string]any{"id": msg.ID, "result": result})
			_ = conn.Write(context.Background(), websocket.MessageText, reply)
		}
	}))
	t.Cleanup(func() { f.drop(); f.server.Close() })
	return f
}

func (f *fakeChrome) endpoint() string {
	return "ws" + strings.TrimPrefix(f.server.URL, "http") + "/devtools/browser"
}

// drop closes every connection, as Chrome does when it quits.
func (f *fakeChrome) drop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, conn := range f.conns {
		_ = conn.CloseNow()
	}
	f.conns = nil
}

type bridgeRig struct {
	b      *Bridge
	r      *harness.Registry
	s      *store.Store
	chrome *fakeChrome
	mu     sync.Mutex
	fail   error // what detection reports instead of the fake Chrome
}

func (g *bridgeRig) setFail(err error) {
	g.mu.Lock()
	g.fail = err
	g.mu.Unlock()
}

func newBridgeRig(t *testing.T, fail error) *bridgeRig {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := &bridgeRig{r: harness.New(s), s: s, chrome: newFakeChrome(t), fail: fail}
	g.b = New(g.r)
	g.b.detect = func(context.Context, Options) (target, error) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.fail != nil {
			return target{}, g.fail
		}
		return target{Endpoint: g.chrome.endpoint()}, nil
	}
	if err := g.b.Start(Options{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.r.SetPaused(true); g.b.Close(); g.r.WaitBackground(); s.Close() })
	waitFor(t, func() bool { return g.b.Status().Tools > 0 })
	return g
}

func (g *bridgeRig) invoke(name, arguments string) (harness.Output, store.Call, error) {
	return g.r.Invoke(context.Background(), name, harness.Invocation{Session: "agent", Client: "test", Arguments: json.RawMessage(arguments)})
}

func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBridgeListsToolsAndConnectsOnFirstCall(t *testing.T) {
	g := newBridgeRig(t, nil)
	waitFor(t, func() bool { return g.b.Status().State == "ready" })
	catalog := chromeCatalog()
	specs := map[string]harness.Spec{}
	for _, spec := range g.r.Specs() {
		specs[spec.Name] = spec
	}
	if len(specs) != len(catalog) || g.b.Status().Tools != len(catalog) {
		t.Fatalf("published %d of %d tools, status %+v", len(specs), len(catalog), g.b.Status())
	}
	for _, tool := range catalog {
		spec, ok := specs["chrome_"+tool.name]
		if !ok || spec.Category != "browser" || spec.Mutating == tool.readOnly || spec.Parallel || (spec.Group == "advanced") != tool.advanced || spec.InputSchema["type"] != "object" || spec.Description == "" {
			t.Fatalf("bad spec for %s: %+v", tool.name, spec)
		}
	}
	// Listing tools and watching for Chrome never opens the connection Chrome asks the user about.
	g.b.Refresh()
	time.Sleep(50 * time.Millisecond)
	if n := g.chrome.dials.Load(); n != 0 {
		t.Fatalf("connected to Chrome %d times before a tool was called", n)
	}
	out, call, err := g.invoke("chrome_list_pages", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(out.MCPResult)
	if !strings.Contains(text, "1: https://example.com/ (Example)") || strings.Contains(text, "sw.js") || strings.Contains(text, "devtools://") {
		t.Fatalf("wrong page list:\n%s", text)
	}
	saved, err := g.s.Get(call.ID)
	if err != nil || saved.Status != "success" || !strings.Contains(string(saved.Result), "example.com") {
		t.Fatal(saved, err)
	}
	// The connection is kept: Chrome asks for permission once, not on every call.
	for i := 0; i < 3; i++ {
		if _, _, err = g.invoke("chrome_list_pages", `{}`); err != nil {
			t.Fatal(err)
		}
	}
	if n := g.chrome.dials.Load(); n != 1 {
		t.Fatalf("connected to Chrome %d times for 4 calls", n)
	}
	// A failing tool reports why, and the connection survives it.
	_, call, err = g.invoke("chrome_take_snapshot", `{}`)
	if err == nil || call.Status != "error" || harness.ErrorCode(err) != "browser_tool_failed" || !strings.Contains(err.Error(), "pageId") {
		t.Fatal("an unselected page was not reported", err, call.Status)
	}
	if _, _, err = g.invoke("chrome_click", `{"uid": 5}`); err == nil || harness.ErrorCode(err) != "invalid_arguments" {
		t.Fatal("wrongly typed arguments were accepted", err)
	}

	g.r.Enable("browser", false)
	_, call, err = g.invoke("chrome_list_pages", `{}`)
	if err == nil || call.Status != "denied" {
		t.Fatal("disabled browser accepted a call")
	}
	g.b.Refresh()
	waitFor(t, func() bool { return g.b.Status().State == "disabled" })
	g.b.mu.Lock()
	connected := g.b.browser != nil
	g.b.mu.Unlock()
	if len(g.r.Specs()) != 0 || connected {
		t.Fatal("a disabled browser is still published or connected")
	}
	g.r.Enable("browser", true)
	g.b.Refresh()
	waitFor(t, func() bool { return g.b.Status().State == "ready" && len(g.r.Specs()) == len(catalog) })
}

func TestBridgeReconnectsAfterChromeClosesAndDropsControlWhenPaused(t *testing.T) {
	g := newBridgeRig(t, nil)
	waitFor(t, func() bool { return g.b.Status().State == "ready" })
	if _, _, err := g.invoke("chrome_list_pages", `{}`); err != nil {
		t.Fatal(err)
	}
	// One missed probe keeps the working connection and the ready status.
	g.setFail(errors.New("probe timed out"))
	g.b.reconcile(context.Background())
	if g.b.Status().State != "ready" {
		t.Fatal("a missed probe took a connected Chrome away", g.b.Status())
	}
	if _, _, err := g.invoke("chrome_list_pages", `{}`); err != nil || g.chrome.dials.Load() != 1 {
		t.Fatal("a missed probe dropped the connection", err, g.chrome.dials.Load())
	}
	g.setFail(nil)
	// Chrome quits and comes back: the next call connects again by itself.
	g.chrome.drop()
	waitFor(t, func() bool {
		g.b.mu.Lock()
		defer g.b.mu.Unlock()
		return g.b.browser == nil || !g.b.browser.conn.alive()
	})
	if _, _, err := g.invoke("chrome_list_pages", `{}`); err != nil || g.chrome.dials.Load() != 2 {
		t.Fatal("did not reconnect after Chrome closed the connection", err, g.chrome.dials.Load())
	}
	// A cancelled call does not cost the connection.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := g.r.Invoke(ctx, "chrome_list_pages", harness.Invocation{Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("a cancelled call succeeded")
	}
	if _, _, err := g.invoke("chrome_list_pages", `{}`); err != nil || g.chrome.dials.Load() != 2 {
		t.Fatal("a cancelled call dropped the connection", err, g.chrome.dials.Load())
	}
	g.r.SetPaused(true)
	_, call, err := g.invoke("chrome_list_pages", `{}`)
	if err == nil || call.Status != "denied" {
		t.Fatal("paused call accepted")
	}
	g.b.Refresh()
	waitFor(t, func() bool { g.b.mu.Lock(); defer g.b.mu.Unlock(); return g.b.browser == nil })
}

func TestToolsStayListedWhileChromeIsNotReady(t *testing.T) {
	g := newBridgeRig(t, ErrDebugPermission)
	count := len(chromeCatalog())
	waitFor(t, func() bool { return g.b.Status().State == "permission_required" })
	if status := g.b.Status(); status.Tools != count || len(g.r.Specs()) != count {
		t.Fatal("missing waiting catalog", status)
	}
	_, call, err := g.invoke("chrome_list_pages", `{}`)
	if err == nil || call.Status != "error" || harness.ErrorCode(err) != "browser_not_ready" || !strings.Contains(err.Error(), "授权") {
		t.Fatal("unready browser call was accepted", err, call.Status)
	}
	g.setFail(fmt.Errorf("等待 Chrome 开启远程调试"))
	g.b.reconcile(context.Background())
	if g.b.Status().State != "waiting" || len(g.r.Specs()) != count {
		t.Fatal("catalog disappeared while Chrome was closed")
	}
	if g.chrome.dials.Load() != 0 {
		t.Fatal("connected to a Chrome that was not detected")
	}
	g.setFail(nil)
	g.b.reconcile(context.Background())
	if g.b.Status().State != "ready" {
		t.Fatal("did not notice Chrome", g.b.Status())
	}
	if _, _, err = g.invoke("chrome_list_pages", `{}`); err != nil {
		t.Fatal("connected call failed", err)
	}
}

// The schemas are written by hand: each must be an object schema whose required
// names exist, with a description on every property.
func TestCatalogSchemasAreConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, tool := range chromeCatalog() {
		if seen[tool.name] || !toolName.MatchString("chrome_"+tool.name) || tool.run == nil {
			t.Fatalf("bad tool %q", tool.name)
		}
		seen[tool.name] = true
		properties, _ := tool.schema["properties"].(props)
		for name, raw := range properties {
			property, _ := raw.(map[string]any)
			if property == nil || property["type"] == nil || property["description"] == "" || property["description"] == nil {
				t.Fatalf("%s.%s has no type or description", tool.name, name)
			}
		}
		for _, name := range tool.schema["required"].([]string) {
			if properties[name] == nil {
				t.Fatalf("%s requires %q, which it does not define", tool.name, name)
			}
		}
	}
}

func TestLocalURLAndChromeProbe(t *testing.T) {
	for _, raw := range []string{"https://127.0.0.1:9222", "http://example.com:9222", "http://127.0.0.1:9222/path", "http://user:pw@127.0.0.1:9222", "http://127.0.0.1:9222?x=1", "http://localhost"} {
		if _, err := localURL(raw); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	var endpoint string
	var remote, redirect bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirect {
			http.Redirect(w, r, "http://example.com", 302)
			return
		}
		ws := "ws" + strings.TrimPrefix(endpoint, "http") + "/devtools/browser/test"
		if remote {
			ws = "ws://example.com:9222/devtools/browser/test"
		}
		json.NewEncoder(w).Encode(map[string]any{"Browser": "Chrome/150.0", "webSocketDebuggerUrl": ws})
	}))
	defer server.Close()
	endpoint = server.URL
	target, err := probeURL(context.Background(), endpoint)
	if err != nil || !strings.HasPrefix(target.Endpoint, "ws://127.0.0.1:") || !strings.HasSuffix(target.Endpoint, "/devtools/browser/test") {
		t.Fatal(target, err)
	}
	remote = true
	if _, err := probeURL(context.Background(), endpoint); err == nil {
		t.Fatal("accepted remote WebSocket")
	}
	remote = false
	redirect = true
	if _, err := probeURL(context.Background(), endpoint); err == nil {
		t.Fatal("followed redirect")
	}
}

func TestDiscoveryRequiresChromeAndLiveDebugging(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "chrome")
	if err := os.WriteFile(binary, []byte("test binary marker"), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	marker := filepath.Join(dir, "DevToolsActivePort")
	if err := os.WriteFile(marker, []byte(port+"\n/devtools/browser/test"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{UserDataDir: dir}
	if _, err := discoverAt(context.Background(), opts, nil, []string{dir}); err == nil {
		t.Fatal("browser not installed")
	}
	target, err := discoverAt(context.Background(), opts, []string{binary}, []string{dir})
	if err != nil || target.Endpoint != "ws://127.0.0.1:"+port+"/devtools/browser/test" {
		t.Fatal(target, err)
	}
	listener.Close()
	if _, err := discoverAt(context.Background(), opts, []string{binary}, []string{dir}); err == nil {
		t.Fatal("stale debugging file accepted")
	}
	for _, bad := range []string{"0\n/devtools/browser/test", "65536\n/devtools/browser/test", "1234\nhttp://example.com"} {
		os.WriteFile(marker, []byte(bad), 0600)
		if _, err := discoverAt(context.Background(), opts, []string{binary}, []string{dir}); err == nil {
			t.Fatal("invalid marker accepted", bad)
		}
	}
}
