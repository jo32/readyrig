package server

import (
	"computer-use-server/internal/buildinfo"
	"computer-use-server/internal/chromemcp"
	"computer-use-server/internal/cliinstall"
	"computer-use-server/internal/cloud"
	"computer-use-server/internal/computer"
	"computer-use-server/internal/harness"
	"computer-use-server/internal/localopen"
	"computer-use-server/internal/store"
	"computer-use-server/internal/tunnel"
	"computer-use-server/internal/update"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed assets
var assets embed.FS

type Server struct {
	Registry                                  *harness.Registry
	Projects                                  *harness.Projects
	Privacy                                   *harness.Privacy
	Store                                     *store.Store
	Computer                                  *computer.Computer
	Chrome                                    *chromemcp.Bridge
	Safari                                    *chromemcp.SafariBridge
	Updates                                   *update.Manager
	Tunnel                                    *tunnel.Manager
	Cloud                                     *cloud.Client
	CLI                                       *cliinstall.Status
	LocalCLI                                  *LocalCLI
	SaveCapability                            func(string, bool) error
	Workspace, GatewayAddr, AccessPath, UIKey string
	AllowedIPs                                []*net.IPNet
	mu                                        sync.Mutex
	capabilityMu                              sync.Mutex
	rateStart                                 time.Time
	rateCount                                 int
	sessions                                  map[string]mcpSession
	inflight                                  map[string]context.CancelFunc
	openLocalPath                             localPathOpener
	openSystemSettings                        systemSettingsOpener
	requestPermission                         func(kind string)
}

// LocalCLI identifies the command and instance to use for local configuration.
// It contains no connection credentials and is excluded from the public console.
type LocalCLI struct {
	Command string `json:"command"`
	DataDir string `json:"data_dir"`
	Mode    string `json:"mode"`
}

type mcpSession struct {
	Client string
	At     time.Time
}

func (s *Server) Gateway() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/tools", func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256([]byte(s.UIKey + "|" + s.AccessPath))
		w.Header().Set("X-Readyrig-Instance", fmt.Sprintf("%x", sum[:16]))
		write(w, s.Registry.Specs())
	})
	mux.HandleFunc("POST /api/v1/tools/{name}", s.invoke)
	for path, name := range map[string]string{"bash/exec": "exec_command", "bash/stdin": "write_stdin", "fs/read": "read_file", "fs/write": "write_file", "fs/list": "list_directory", "fs/search": "search_files", "computer/action": "computer_action", "computer/screenshot": "computer_screenshot", "fs/edit": "edit_file", "fs/glob": "glob", "computer/ui-tree": "computer_ui_tree", "computer/app": "computer_app", "computer/clipboard": "computer_clipboard"} {
		name := name
		mux.HandleFunc("POST /api/v1/"+path, func(w http.ResponseWriter, r *http.Request) { r.SetPathValue("name", name); s.invoke(w, r) })
	}
	mux.HandleFunc("GET /api/v1/openapi.json", s.openapi)
	mux.HandleFunc("POST /mcp", s.mcp)
	mux.HandleFunc("GET /mcp", s.mcpStream)
	mux.Handle("/app/", http.StripPrefix("/app", s.PublicUI()))
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			problem(w, 403, fmt.Errorf("公网控制台仅供查看"))
			return
		}
		http.Redirect(w, r, "/"+s.requestAccessPath(r)+"/app/", http.StatusTemporaryRedirect)
	})
	return s.gatewayGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix("/"+s.requestAccessPath(r), headers(mux)).ServeHTTP(w, r)
	}))
}
func (s *Server) invoke(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*1024*1024))
	if err != nil {
		problem(w, 400, err)
		return
	}
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	session, client := r.Header.Get("X-Session-ID"), r.Header.Get("X-Client-Name")
	if len(session) > 128 || len(client) > 128 {
		problem(w, 400, fmt.Errorf("session/client header too long"))
		return
	}
	status, body := s.runTool(r.Context(), r.PathValue("name"), raw, session, client)
	w.WriteHeader(status)
	write(w, body)
}

// runTool executes one tool call and returns the HTTP status and body the gateway
// replies with. The public gateway and the opt-in cloud relay share it, so both
// apply the same capability, pause and project checks and the same logging.
func (s *Server) runTool(ctx context.Context, name string, raw []byte, session, client string) (int, map[string]any) {
	out, call, err := s.Registry.Invoke(ctx, name, harness.Invocation{Session: session, Client: client, Arguments: raw})
	status := 200
	if err != nil {
		status = 422
		if call.Status == "denied" {
			status = 423
		}
	}
	body := map[string]any{"call_id": call.ID, "result": out.Value, "error": call.Error, "status": call.Status}
	if err != nil {
		body["error_code"] = harness.ErrorCode(err)
	}
	if len(out.Images) > 0 {
		body["images"] = out.Images
	}
	if err == nil && out.Failure != "" {
		// A command that exited non-zero is a result the agent reads, not a failed call.
		body["status"], body["error"] = "success", ""
	}
	sid := harness.SessionOrDefault(session)
	if notices := harness.WithoutOwn(append(s.Registry.TakeNotices(sid), s.Registry.ProgressFor(sid, name)...), out.Value); len(notices) > 0 {
		body["notices"] = NoticeValues(notices)
	}
	return status, body
}
func (s *Server) UI() http.Handler {
	mux := http.NewServeMux()
	s.updateRoutes(mux)
	s.projectRoutes(mux)
	s.permissionRoutes(mux)
	s.localOpenRoutes(mux)
	s.tunnelRoutes(mux)
	s.cloudRoutes(mux)
	s.privacyRoutes(mux)
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		paused, enabled := s.Registry.State()
		summary, err := s.Store.Summary()
		if err != nil {
			problem(w, 500, err)
			return
		}
		sessions, err := s.Store.Sessions()
		if err != nil {
			problem(w, 500, err)
			return
		}
		chrome := chromemcp.Status{State: "disabled", Message: "Chrome MCP 未启动"}
		if s.Chrome != nil {
			chrome = s.Chrome.Status()
		}
		safari := chromemcp.Status{State: "disabled", Message: "Safari MCP 未启动"}
		if s.Safari != nil {
			safari = s.Safari.Status()
		}
		remote := isPublicUI(r)
		gateway, origin := s.connection(r)
		var updates any
		var cloudState any
		var cliState any
		var localCLI any
		if !remote {
			updates = s.updateStatus()
			cloudState = s.cloudStatus()
			cliState = s.CLI
			localCLI = s.LocalCLI
		}
		write(w, map[string]any{"public": remote, "paused": paused, "enabled": enabled, "summary": summary, "sessions": sessions, "tools": s.Registry.Specs(), "permissions": s.Computer.Permissions(), "workspace": s.activeWorkspace(), "project_access": s.projectState(), "local_open": localopen.Supported(), "gateway": gateway, "gateway_origin": origin, "version": buildinfo.Version, "chrome": chrome, "safari": safari, "update": updates, "cloud": cloudState, "cli": cliState, "local_cli": localCLI, "tunnel": s.tunnelStatus(remote), "privacy": s.privacyState(remote)})
	})
	mux.HandleFunc("POST /api/chrome/refresh", func(w http.ResponseWriter, r *http.Request) {
		if s.Chrome != nil {
			s.Chrome.Refresh()
		}
		write(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/calls", func(w http.ResponseWriter, r *http.Request) {
		f := filter(r)
		f.Lightweight = r.URL.Query().Get("view") == "summary"
		calls, total, err := s.Store.List(f)
		if err != nil {
			problem(w, 500, err)
			return
		}
		write(w, map[string]any{"calls": calls, "total": total})
	})
	mux.HandleFunc("GET /api/calls/{id}", func(w http.ResponseWriter, r *http.Request) {
		call, err := s.Store.Get(r.PathValue("id"))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				problem(w, 404, fmt.Errorf("call not found"))
			} else {
				problem(w, 500, err)
			}
			return
		}
		detail := map[string]any{"call": call}
		var args struct {
			FrameID string `json:"frame_id"`
		}
		if json.Unmarshal(call.Arguments, &args) == nil && args.FrameID != "" {
			before, err := s.Store.Frame(call.Session, args.FrameID)
			if err == nil {
				detail["before"] = before
			} else if !errors.Is(err, sql.ErrNoRows) {
				problem(w, 500, err)
				return
			}
		}
		write(w, detail)
	})
	mux.HandleFunc("POST /api/calls/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !s.Registry.Cancel(r.PathValue("id")) {
			problem(w, 409, fmt.Errorf("调用已经结束，或不在本次运行中"))
			return
		}
		write(w, map[string]bool{"cancel_requested": true})
	})
	mux.HandleFunc("POST /api/pause", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Paused bool `json:"paused"`
		}
		if !decode(w, r, &v) {
			return
		}
		s.Registry.SetPaused(v.Paused)
		if s.Chrome != nil {
			s.Chrome.Refresh()
		}
		write(w, map[string]bool{"paused": v.Paused})
	})
	mux.HandleFunc("POST /api/capability", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Category string `json:"category"`
			Enabled  *bool  `json:"enabled"`
		}
		if !decode(w, r, &v) {
			return
		}
		if !capabilityCategory(v.Category) || v.Enabled == nil {
			problem(w, 400, errors.New("category and enabled are required for a known capability"))
			return
		}
		if err := s.setCapability(v.Category, *v.Enabled); err != nil {
			problem(w, 500, err)
			return
		}
		write(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/tools/{name}", s.invoke)
	mux.HandleFunc("GET /api/connection", func(w http.ResponseWriter, r *http.Request) {
		gateway, origin := s.connection(r)
		write(w, map[string]any{"gateway": gateway, "gateway_origin": origin, "tunnel": s.tunnelStatus(isPublicUI(r))})
	})
	mux.HandleFunc("GET /api/export", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="readyrig-calls.ndjson"`)
		w.Header().Set("Trailer", "X-Export-Error")
		started := false
		err := s.Store.Export(r.Context(), filter(r), func(total int) {
			started = true
			w.Header().Set("X-Export-Total", strconv.Itoa(total))
			w.WriteHeader(http.StatusOK)
		}, w)
		if err != nil {
			if !started {
				http.Error(w, "export failed", http.StatusInternalServerError)
			} else {
				w.Header().Set("X-Export-Error", "export failed")
			}
		}
	})
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/screenshots/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if len(name) != 28 || !strings.HasSuffix(name, ".jpg") || strings.ContainsAny(name, "/\\") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=86400")
		http.ServeFile(w, r, filepath.Join(s.Store.Dir, "screenshots", name))
	})
	sub, _ := fs.Sub(assets, "assets")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
	return headers(mux)
}
func filter(r *http.Request) store.Filter {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	return store.Filter{Query: q.Get("q"), Session: q.Get("session"), Category: q.Get("category"), Status: q.Get("status"), Limit: limit, Offset: offset}
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	f, ok := w.(http.Flusher)
	if !ok {
		problem(w, 500, fmt.Errorf("streaming unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "data: connected\n\n")
	f.Flush()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		change := s.Registry.Changed()
		select {
		case <-change:
			fmt.Fprint(w, "data: changed\n\n")
		case <-ticker.C:
			fmt.Fprint(w, ": heartbeat\n\n")
		case <-r.Context().Done():
			return
		}
		f.Flush()
	}
}
func (s *Server) gatewayGuard(next http.Handler) http.Handler {
	return headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.AllowedIPs) > 0 {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(host)
			ok := false
			for _, cidr := range s.AllowedIPs {
				if cidr.Contains(ip) {
					ok = true
				}
			}
			if !ok {
				problem(w, 403, fmt.Errorf("peer IP is not allowed"))
				return
			}
		}
		s.mu.Lock()
		if time.Since(s.rateStart) >= time.Minute {
			s.rateStart = time.Now()
			s.rateCount = 0
		}
		s.rateCount++
		limited := s.rateCount > 240
		s.mu.Unlock()
		if limited {
			w.Header().Set("Retry-After", "60")
			problem(w, 429, fmt.Errorf("rate limit exceeded"))
			return
		}
		// Check the literal first segment before ServeMux can clean or redirect paths.
		segment, rest, found := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
		accepted := validAccessPath(s.AccessPath) && subtle.ConstantTimeCompare([]byte(segment), []byte(s.AccessPath)) == 1
		if s.Tunnel != nil {
			fixed := s.Tunnel.FixedAccessPath()
			accepted = accepted || validAccessPath(fixed) && subtle.ConstantTimeCompare([]byte(segment), []byte(fixed)) == 1
		}
		if !found || !accepted {
			http.NotFound(w, r)
			return
		}
		decoded, err := url.PathUnescape(rest)
		if err != nil || strings.Contains(decoded, "//") || strings.HasPrefix(decoded, "/") {
			http.NotFound(w, r)
			return
		}
		for _, part := range strings.Split(decoded, "/") {
			if part == "." || part == ".." {
				http.NotFound(w, r)
				return
			}
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			// The shared console permits same-origin reads only. Browser requests
			// to the Agent REST/MCP endpoints remain forbidden.
			u, err := url.Parse(origin)
			console := decoded == "app" || strings.HasPrefix(decoded, "app/")
			read := r.Method == http.MethodGet || r.Method == http.MethodHead
			if !console || !read || err != nil || u.Host != r.Host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" {
				problem(w, 403, fmt.Errorf("browser origins are not allowed on the agent gateway"))
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accessPathKey{}, segment)))
	}))
}

// BrowserGuard is only for the loopback dashboard, never the tunnel-facing gateway.
func (s *Server) BrowserGuard(next http.Handler) http.Handler {
	return headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil || host != "127.0.0.1" && host != "localhost" && host != "::1" {
			problem(w, 403, fmt.Errorf("invalid local host"))
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Scheme != "http" || u.Host != r.Host {
				problem(w, 403, fmt.Errorf("cross-origin request rejected"))
				return
			}
		}
		if r.URL.Path == "/api/login" && r.Method == "POST" {
			var in struct{ Key string }
			if !decode(w, r, &in) {
				return
			}
			if subtle.ConstantTimeCompare([]byte(in.Key), []byte(s.UIKey)) != 1 {
				problem(w, 401, fmt.Errorf("invalid dashboard key"))
				return
			}
			http.SetCookie(w, &http.Cookie{Name: dashboardCookieName(r.Host), Value: s.UIKey, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			write(w, map[string]bool{"ok": true})
			return
		}
		// Only the inert shell is public. All data and actions require the dashboard cookie.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			cookie, err := r.Cookie(dashboardCookieName(r.Host))
			if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(s.UIKey)) != 1 {
				problem(w, 401, fmt.Errorf("请使用程序启动时提供的控制台链接"))
				return
			}
		}
		next.ServeHTTP(w, r)
	}))
}

// Cookies do not include a port in their scope. Distinguish parallel local instances.
func dashboardCookieName(host string) string {
	return "adapter_ui_" + strings.NewReplacer(":", "_", ".", "_", "[", "", "]", "").Replace(host)
}
func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/mcp" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		next.ServeHTTP(w, r)
	})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*1024*1024))
	if err == nil {
		err = harness.Decode(b, v)
	}
	if err != nil {
		problem(w, 400, err)
		return false
	}
	return true
}
func write(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
func problem(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	write(w, map[string]string{"error": err.Error()})
}
func Listen(addr string, handler http.Handler) (*http.Server, net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go srv.Serve(ln)
	return srv, ln, nil
}
func Shutdown(s *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.Shutdown(ctx)
}

// NewAccessPath creates a fresh, uniformly distributed 8-character URL credential.
// It is kept in memory only and is regenerated for each application run.
func NewAccessPath() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var path [8]byte
	for i := range path {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		path[i] = alphabet[n.Int64()]
	}
	return string(path[:]), nil
}
func validAccessPath(path string) bool {
	if len(path) != 8 {
		return false
	}
	for _, c := range path {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func (s *Server) GatewayURL() string {
	return strings.TrimRight(s.GatewayAddr, "/") + "/" + s.AccessPath
}
