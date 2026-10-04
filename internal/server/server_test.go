package server

import (
	"bytes"
	"computer-use-server/internal/cliinstall"
	"computer-use-server/internal/computer"
	"computer-use-server/internal/harness"
	"computer-use-server/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) *Server {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := harness.New(s, "aSsxba11")
	files, err := harness.NewFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { files.Close() })
	files.Register(r)
	return &Server{Registry: r, Store: s, Computer: computer.New(s.Dir), AccessPath: "aSsxba11", GatewayAddr: "http://127.0.0.1:7332", UIKey: "dashboard-secret"}
}
func request(h http.Handler, method, path, body, accessPath string) *httptest.ResponseRecorder {
	if accessPath != "" {
		path = "/" + accessPath + path
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDashboardAllowsScreenshotBlobImages(t *testing.T) {
	s := fixture(t)
	for name, handler := range map[string]http.Handler{
		"local":   s.BrowserGuard(s.UI()),
		"gateway": s.Gateway(),
	} {
		t.Run(name, func(t *testing.T) {
			path := "http://127.0.0.1:7331/"
			if name == "gateway" {
				path += s.AccessPath + "/app/"
			}
			w := request(handler, "GET", path, "", "")
			if w.Code != http.StatusOK {
				t.Fatalf("dashboard returned %d", w.Code)
			}
			directives := map[string]string{}
			for _, directive := range strings.Split(w.Header().Get("Content-Security-Policy"), ";") {
				fields := strings.Fields(directive)
				if len(fields) > 0 {
					directives[fields[0]] = strings.Join(fields[1:], " ")
				}
			}
			if directives["img-src"] != "'self' data: blob:" {
				t.Fatalf("screenshot object URLs must be allowed: %v", directives)
			}
			for _, directive := range []string{"default-src", "script-src", "connect-src"} {
				if directives[directive] != "'self'" {
					t.Fatalf("%s must remain restricted to self: %v", directive, directives)
				}
			}
		})
	}
}

func TestCLIInstallationStatusIsLocalOnly(t *testing.T) {
	s := fixture(t)
	s.CLI = &cliinstall.Status{State: "installed", Path: "/Users/private/.local/bin/readyrig"}
	s.LocalCLI = &LocalCLI{Command: "/Applications/ReadyRig.app/Contents/Helpers/readyrig", DataDir: "/Users/private/custom-data", Mode: "desktop"}
	local := request(s.UI(), "GET", "/api/state", "", "")
	if local.Code != 200 || !strings.Contains(local.Body.String(), s.CLI.Path) || !strings.Contains(local.Body.String(), s.LocalCLI.DataDir) || !strings.Contains(local.Body.String(), s.LocalCLI.Command) {
		t.Fatal("local installation status missing", local.Code, local.Body.String())
	}
	public := request(s.Gateway(), "GET", "/app/api/state", "", s.AccessPath)
	if public.Code != 200 || strings.Contains(public.Body.String(), s.CLI.Path) || strings.Contains(public.Body.String(), s.LocalCLI.Command) || strings.Contains(public.Body.String(), s.LocalCLI.DataDir) {
		t.Fatal("CLI installation status exposed publicly", public.Code, public.Body.String())
	}
	var publicState map[string]any
	if err := json.Unmarshal(public.Body.Bytes(), &publicState); err != nil || publicState["local_cli"] != nil {
		t.Fatal("public console received local CLI context", err, publicState)
	}
}
func TestGatewayAuthAndBoundaries(t *testing.T) {
	s := fixture(t)
	h := s.Gateway()
	if w := request(h, "GET", "/api/v1/tools", "", "bad"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", "/api/v1/tools", "", s.AccessPath); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(h, "POST", "/api/pause", `{"paused":false}`, s.AccessPath); w.Code != 404 {
		t.Fatal("remote can reach control routes", w.Code)
	}
	r := httptest.NewRequest("POST", "/"+s.AccessPath+"/api/v1/fs/list", strings.NewReader(`{"path":"."}`))
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("origin accepted")
	}
	w = request(h, "POST", "/api/v1/fs/list", `{"path":"."}`, s.AccessPath)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.Registry.SetPaused(true)
	w = request(h, "POST", "/api/v1/fs/list", `{"path":"."}`, s.AccessPath)
	if w.Code != 423 {
		t.Fatal("paused tools execute")
	}
}
func TestDashboardAuthenticationAndRebinding(t *testing.T) {
	s := fixture(t)
	h := s.BrowserGuard(s.UI())
	w := request(h, "GET", "http://127.0.0.1:7331/api/state", "", "")
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	w = request(h, "POST", "http://127.0.0.1:7331/api/login", `{"key":"dashboard-secret"}`, "")
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	r := httptest.NewRequest("GET", "http://127.0.0.1:7331/api/state", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:7331/api/pause", strings.NewReader(`{"paused":false}`))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("csrf accepted")
	}
	w = request(h, "GET", "http://evil.example:7331/api/state", "", "")
	if w.Code != 403 {
		t.Fatal("DNS rebinding accepted")
	}
}
func TestMCPInitializeListAndCall(t *testing.T) {
	s := fixture(t)
	h := s.Gateway()
	w := request(h, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"test"}}}`, s.AccessPath)
	sid := w.Header().Get("Mcp-Session-Id")
	if w.Code != 200 || sid == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, method := range []string{"tools/list", "tools/call"} {
		body := map[string]any{"jsonrpc": "2.0", "id": 2, "method": method, "params": map[string]any{"name": "list_directory", "arguments": map[string]any{"path": "."}}}
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/"+s.AccessPath+"/mcp", bytes.NewReader(b))
		r.Header.Set("Mcp-Session-Id", sid)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), `"isError":true`) {
			t.Fatal(w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out["result"] == nil {
			t.Fatal(w.Body.String())
		}
	}
}

func TestDetailBeforeFrameAndLocalCancellationBoundary(t *testing.T) {
	s := fixture(t)
	before := store.Call{ID: "before", Session: "s", Tool: "computer_screenshot", Category: "computer", Status: "success", Result: json.RawMessage(`{"frame_id":"frame-one","image_size":[1280,720]}`), Screenshot: "frame.jpg"}
	after := store.Call{ID: "after", Session: "s", Tool: "computer_action", Category: "computer", Status: "success", Arguments: json.RawMessage(`{"frame_id":"frame-one","coordinate":[20,30]}`), Result: json.RawMessage(`{"stdout":"complete output"}`)}
	s.Store.Save(before)
	s.Store.Save(after)
	w := request(s.UI(), "GET", "/api/calls/after", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"before":`) || !strings.Contains(w.Body.String(), "complete output") {
		t.Fatal(w.Code, w.Body.String())
	}
	// A same frame reference in a different session cannot associate screenshots.
	after.ID = "other-session"
	after.Session = "other"
	s.Store.Save(after)
	w = request(s.UI(), "GET", "/api/calls/other-session", "", "")
	if strings.Contains(w.Body.String(), `"before":`) {
		t.Fatal("cross-session frame linked")
	}
	w = request(s.UI(), "GET", "/api/calls/missing", "", "")
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	w = request(s.Gateway(), "POST", "/api/calls/after/cancel", "{}", s.AccessPath)
	if w.Code != 404 {
		t.Fatal("remote reached local cancellation", w.Code)
	}
	w = request(s.UI(), "POST", "/api/calls/after/cancel", "{}", "")
	if w.Code != 409 {
		t.Fatal("completed cancellation claimed success", w.Code)
	}
}

func TestMCPExternalResultPreservesBlocksAndSchemas(t *testing.T) {
	s := fixture(t)
	result := map[string]any{"content": []any{map[string]any{"type": "image", "mimeType": "image/png", "data": "aGVsbG8="}, map[string]any{"type": "text", "text": "browser failure"}}, "structuredContent": map[string]any{"reason": "test"}, "isError": true}
	s.Registry.ReplaceCategory("browser", []harness.Tool{{Spec: harness.Spec{Name: "chrome_test", Category: "browser", InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object"}, Annotations: map[string]any{"readOnlyHint": true}}, External: true, Run: func(context.Context, harness.Invocation) (harness.Output, error) {
		return harness.Output{Value: result, MCPResult: result}, fmt.Errorf("browser failure")
	}}})
	h := s.Gateway()
	init := request(h, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, s.AccessPath)
	rpc := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/"+s.AccessPath+"/mcp", strings.NewReader(body))
		r.Header.Set("Mcp-Session-Id", init.Header().Get("Mcp-Session-Id"))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	list := rpc(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if !strings.Contains(list.Body.String(), `"outputSchema"`) || !strings.Contains(list.Body.String(), `"readOnlyHint":true`) {
		t.Fatal(list.Body.String())
	}
	w := rpc(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"chrome_test","arguments":{}}}`)
	var reply struct {
		Result map[string]any `json:"result"`
	}
	json.Unmarshal(w.Body.Bytes(), &reply)
	if reply.Result["isError"] != true || reply.Result["structuredContent"] == nil || len(reply.Result["content"].([]any)) != 2 {
		t.Fatal(w.Body.String())
	}
	if w := request(h, "POST", "/api/chrome/refresh", `{}`, s.AccessPath); w.Code != 404 {
		t.Fatal("remote can reconfigure Chrome", w.Code)
	}
}

func TestHelpAvailableViaRESTAndMCPWhilePaused(t *testing.T) {
	s := fixture(t)
	s.Registry.RegisterHelp()
	s.Registry.SetPaused(true)
	h := s.Gateway()
	w := request(h, "POST", "/api/v1/tools/help", `{"name":"read_file"}`, s.AccessPath)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"unavailable_reason":"control_paused"`) || !strings.Contains(w.Body.String(), `"inputSchema"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	init := request(h, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, s.AccessPath)
	r := httptest.NewRequest("POST", "/"+s.AccessPath+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"help","arguments":{}}}`))
	r.Header.Set("Mcp-Session-Id", init.Header().Get("Mcp-Session-Id"))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var response struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result.IsError || len(response.Result.Content) != 1 {
		t.Fatal(w.Body.String())
	}
	var payload struct {
		Result harness.HelpResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(response.Result.Content[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Result.Paused || payload.Result.Total != 1 || payload.Result.Tools[0].Name != "help" {
		t.Fatal(payload)
	}
}

func TestAccessPathRejectsMissingWrongAndAmbiguousPaths(t *testing.T) {
	s := fixture(t)
	h := s.Gateway()
	for _, path := range []string{"/api/v1/tools", "/mcp", "/wrong123/api/v1/tools", "/ASSXBA11/api/v1/tools", "/aSsxba11extra/api/v1/tools", "//aSsxba11/api/v1/tools", "/%61Ssxba11/api/v1/tools", "/aSsxba11//api/v1/tools", "/aSsxba11/x/../api/v1/tools", "/aSsxba11/%2e%2e/api/v1/tools", "/aSsxba11%2fapi/v1/tools", "/aSsxba11"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer old-token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 || w.Header().Get("Location") != "" {
			t.Fatalf("%s: code=%d redirect=%s", path, w.Code, w.Header().Get("Location"))
		}
		if strings.Contains(w.Body.String(), s.AccessPath) {
			t.Fatal("error exposes current access path")
		}
	}
	w := request(h, "GET", "/api/v1/tools", "", s.AccessPath)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatal(w.Code, w.Header())
	}
	s.AccessPath = ""
	if w := request(s.Gateway(), "GET", "/api/v1/tools", "", ""); w.Code != 404 {
		t.Fatal("empty access path opens gateway")
	}
}
func TestPathConnectionConfigAndOpenAPI(t *testing.T) {
	s := fixture(t)
	w := request(s.UI(), "GET", "/api/connection", "", "")
	var conn map[string]any
	json.Unmarshal(w.Body.Bytes(), &conn)
	if conn["gateway"] != "http://127.0.0.1:7332/aSsxba11" || conn["gateway_origin"] != s.GatewayAddr || conn["token"] != nil {
		t.Fatal(conn)
	}
	w = request(s.Gateway(), "GET", "/api/v1/openapi.json", "", s.AccessPath)
	var spec map[string]any
	json.Unmarshal(w.Body.Bytes(), &spec)
	servers := spec["servers"].([]any)
	if servers[0].(map[string]any)["url"] != "/aSsxba11" || strings.Contains(w.Body.String(), "bearerAuth") || strings.Contains(w.Body.String(), "securitySchemes") {
		t.Fatal(w.Body.String())
	}
	w = request(s.Gateway(), "GET", "/api/connection", "", s.AccessPath)
	if w.Code != 404 {
		t.Fatal("agent reached connection management")
	}
	// Unknown paths count toward the same request limit, too.
	s.rateCount = 240
	s.rateStart = time.Now()
	if w = request(s.Gateway(), "GET", "/api/v1/tools", "", "wrong123"); w.Code != 429 {
		t.Fatal("invalid paths bypass rate limit", w.Code)
	}
}
func TestGeneratedAccessPathFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		path, err := NewAccessPath()
		if err != nil || !validAccessPath(path) || seen[path] {
			t.Fatalf("unexpected access path: %q %v", path, err)
		}
		seen[path] = true
	}
}

func TestDashboardCookiesDoNotClobberOtherPorts(t *testing.T) {
	first, second := fixture(t), fixture(t)
	second.UIKey = "second-dashboard-secret"
	login := func(s *Server, host string) *http.Cookie {
		w := request(s.BrowserGuard(s.UI()), "POST", "http://"+host+"/api/login", `{"key":"`+s.UIKey+`"}`, "")
		if w.Code != 200 || len(w.Result().Cookies()) != 1 {
			t.Fatal(w.Code)
		}
		return w.Result().Cookies()[0]
	}
	a := login(first, "127.0.0.1:7331")
	b := login(second, "127.0.0.1:7631")
	if a.Name == b.Name {
		t.Fatal("different ports overwrite each other's dashboard cookies")
	}
	for _, instance := range []struct {
		Server *Server
		Host   string
	}{{first, "127.0.0.1:7331"}, {second, "127.0.0.1:7631"}} {
		req := httptest.NewRequest("GET", "http://"+instance.Host+"/api/connection", nil)
		req.AddCookie(a)
		req.AddCookie(b)
		w := httptest.NewRecorder()
		instance.Server.BrowserGuard(instance.Server.UI()).ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(instance.Host, w.Code)
		}
	}
}

func TestExportStreamsAllMatchingFullRecords(t *testing.T) {
	s := fixture(t)
	for i := 0; i < 503; i++ {
		category := "terminal"
		if i == 502 {
			category = "files"
		}
		if err := s.Store.Save(store.Call{ID: fmt.Sprint(i), Category: category, Started: time.Now(), Arguments: json.RawMessage(`{"content":"input"}`), Result: json.RawMessage(`{"text":"output"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	w := request(s.UI(), "GET", "/api/export?category=terminal&limit=40&offset=40&view=summary", "", "")
	if w.Code != 200 || w.Header().Get("X-Export-Total") != "502" {
		t.Fatalf("status=%d header=%v", w.Code, w.Header())
	}
	if bytes.Count(w.Body.Bytes(), []byte("\n")) != 502 || strings.Count(w.Body.String(), `"content":"input"`) != 502 || strings.Count(w.Body.String(), `"text":"output"`) != 502 {
		t.Fatal("incomplete export")
	}
}
