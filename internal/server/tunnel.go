package server

import (
	"computer-use-server/internal/tunnel"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type shareStatus struct {
	tunnel.Status
	Gateway string                `json:"gateway,omitempty"`
	Console string                `json:"console,omitempty"`
	MCP     string                `json:"mcp,omitempty"`
	Fixed   *tunnel.FixedSettings `json:"fixed,omitempty"`
}

func (s *Server) tunnelStatus(remote bool) shareStatus {
	status := tunnel.Status{State: "stopped", Message: "公网分享未开启"}
	if s.Tunnel != nil {
		status = s.Tunnel.Status()
	}
	if remote {
		status.Logs, status.Executable, status.Error = nil, "", ""
	}
	out := shareStatus{Status: status}
	if !remote && s.Tunnel != nil {
		settings := s.Tunnel.FixedSettings()
		out.Fixed = &settings
	}
	if status.State == "ready" && status.URL != "" {
		access := s.AccessPath
		if status.Mode == "fixed" {
			access = s.Tunnel.FixedAccessPath()
			if access == "" {
				return out
			}
		}
		out.Gateway = status.URL + "/" + access
		out.Console, out.MCP = out.Gateway+"/app/", out.Gateway+"/mcp"
	}
	return out
}

func (s *Server) StartSharing() error { return s.startSharingMode("quick") }

func (s *Server) startSharingMode(mode string) error {
	if s.Tunnel == nil {
		return errors.New("隧道服务未启动")
	}
	target := s.GatewayAddr
	// A wildcard listener still connects through loopback, never through a LAN IP.
	if u, err := url.Parse(target); err == nil {
		host, port, err := net.SplitHostPort(u.Host)
		if err == nil && (host == "0.0.0.0" || host == "::") {
			host = "127.0.0.1"
			if u.Hostname() == "::" {
				host = "::1"
			}
			u.Host = net.JoinHostPort(host, port)
			target = u.String()
		}
	}
	s.Tunnel.SetAccessPath(s.AccessPath)
	return s.Tunnel.StartMode(target, mode)
}

func (s *Server) tunnelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tunnel", func(w http.ResponseWriter, r *http.Request) { write(w, s.tunnelStatus(false)) })
	mux.HandleFunc("POST /api/tunnel/start", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Mode string `json:"mode"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Mode == "" {
			in.Mode = "quick"
		}
		if err := s.startSharingMode(in.Mode); err != nil {
			problem(w, 409, err)
			return
		}
		write(w, s.tunnelStatus(false))
	})
	mux.HandleFunc("POST /api/tunnel/fixed", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		}
		if !decode(w, r, &in) {
			return
		}
		if s.Tunnel == nil {
			problem(w, 409, errors.New("隧道服务未启动"))
			return
		}
		if err := s.Tunnel.SaveFixed(in.URL, in.Token); err != nil {
			problem(w, 400, err)
			return
		}
		write(w, s.tunnelStatus(false))
	})
	mux.HandleFunc("POST /api/tunnel/stop", func(w http.ResponseWriter, r *http.Request) {
		if s.Tunnel != nil {
			if err := s.Tunnel.Stop(); err != nil {
				problem(w, 409, err)
				return
			}
		}
		write(w, s.tunnelStatus(false))
	})
}

type publicUIKey struct{}

func isPublicUI(r *http.Request) bool { return r.Context().Value(publicUIKey{}) == true }

// PublicUI reuses the display routes under a strict read allowlist. New local
// management routes do not become public when added to UI().
func (s *Server) PublicUI() http.Handler {
	ui := s.UI()
	assets := map[string]bool{"/": true, "/index.html": true, "/icon.svg": true, "/app.js": true, "/window.js": true, "/cloud.js": true, "/cloud.css": true, "/app.css": true, "/controls.css": true, "/window.css": true, "/magpie-base.css": true}
	reads := map[string]bool{"/api/state": true, "/api/connection": true, "/api/calls": true, "/api/export": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			problem(w, 403, errors.New("公网控制台仅供查看，请在本机管理权限与操作"))
			return
		}
		path := r.URL.Path
		detail := strings.HasPrefix(path, "/api/calls/") && !strings.Contains(strings.TrimPrefix(path, "/api/calls/"), "/")
		screenshot := strings.HasPrefix(path, "/api/screenshots/") && !strings.Contains(strings.TrimPrefix(path, "/api/screenshots/"), "/")
		if !assets[path] && !reads[path] && !detail && !screenshot {
			http.NotFound(w, r)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), publicUIKey{}, true))
		// The public console shows the audit log and project folders. In privacy mode
		// they are masked as agents see them, including calls logged before it was on.
		if strings.HasPrefix(path, "/api/") && !screenshot && s.Privacy.Enabled() {
			masked := &maskedWriter{ResponseWriter: w, privacy: s.Privacy}
			defer masked.finish()
			w = masked
		}
		ui.ServeHTTP(w, r)
	})
}

func (s *Server) connection(r *http.Request) (gateway, origin string) {
	if !isPublicUI(r) {
		return s.GatewayURL(), s.GatewayAddr
	}
	if status := s.tunnelStatus(true); status.Gateway != "" {
		return status.Gateway, status.URL
	}
	scheme := "http"
	if r.TLS != nil || strings.HasSuffix(r.Host, ".trycloudflare.com") {
		scheme = "https"
	}
	origin = scheme + "://" + r.Host
	return origin + "/" + s.requestAccessPath(r), origin
}

// The gateway records the validated prefix so public redirects and OpenAPI keep it.
type accessPathKey struct{}

func (s *Server) requestAccessPath(r *http.Request) string {
	if path, ok := r.Context().Value(accessPathKey{}).(string); ok {
		return path
	}
	return s.AccessPath
}
