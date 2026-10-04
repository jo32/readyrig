package server

import (
	"computer-use-server/internal/buildinfo"
	"computer-use-server/internal/cloud"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"runtime"
)

func (s *Server) StartCloud(baseURL string) error {
	s.Cloud = cloud.New(cloud.Options{Dir: filepath.Join(s.Store.Dir, "cloud"), URL: baseURL, Changed: s.Registry.Signal, RedactSecrets: s.Registry.AddSecrets, Snapshot: s.cloudSnapshot, Execute: s.executeCloudCommand, Relay: s.relayCall, TunnelReady: s.tunnelReady})
	return s.Cloud.Start()
}
func (s *Server) cloudSnapshot() any {
	paused, enabled := s.Registry.State()
	relay := cloud.RelayStatus{State: "off"}
	if s.Cloud != nil {
		relay = s.Cloud.Relay()
	}
	return map[string]any{"version": buildinfo.Version, "platform": runtime.GOOS, "paused": paused, "enabled": enabled, "tunnel": s.tunnelStatus(true), "relay": map[string]string{"state": relay.State, "message": relay.Message}}
}

// tunnelReady reports whether public sharing currently has a working link. Relay mode only
// connects while it does not.
func (s *Server) tunnelReady() bool {
	return s.Tunnel != nil && s.Tunnel.Status().State == "ready"
}

// maxRelayArguments matches the gateway's 2 MiB request limit.
const maxRelayArguments = 2 * 1024 * 1024

// relayCall runs a tool call that arrived over the opt-in cloud relay, with the same
// checks and logging as a call through the gateway.
func (s *Server) relayCall(ctx context.Context, call cloud.RelayCall) cloud.RelayReply {
	raw := []byte(call.Arguments)
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte(`{}`)
	}
	if len(raw) > maxRelayArguments {
		return cloud.RelayReply{Status: http.StatusRequestEntityTooLarge, Body: json.RawMessage(`{"error":"tool arguments exceed 2 MiB","status":"error"}`)}
	}
	status, body := s.runTool(ctx, call.Tool, raw, call.Session, call.Client)
	data, err := json.Marshal(body)
	if err != nil {
		return cloud.RelayReply{Status: http.StatusInternalServerError, Body: json.RawMessage(`{"error":"result could not be encoded","status":"error"}`)}
	}
	return cloud.RelayReply{Status: status, Body: data}
}
func (s *Server) executeCloudCommand(cmd cloud.Command) error {
	switch cmd.Kind {
	case "tunnel.start":
		var in struct {
			Mode string `json:"mode"`
		}
		if err := json.Unmarshal(cmd.Payload, &in); err != nil {
			return err
		}
		if in.Mode == "" {
			in.Mode = "quick"
		}
		return s.startSharingMode(in.Mode)
	case "tunnel.stop":
		if s.Tunnel != nil {
			return s.Tunnel.Stop()
		}
		return nil
	case "relay.stop":
		// Turning the relay off is always allowed. Turning it on is not available here:
		// it sends tool data through the cloud service and needs consent on this computer.
		if s.Cloud == nil {
			return nil
		}
		return s.Cloud.SetRelay(false)
	case "control.pause":
		var in struct {
			Paused *bool `json:"paused"`
		}
		if err := json.Unmarshal(cmd.Payload, &in); err != nil {
			return err
		}
		if in.Paused == nil {
			return errors.New("缺少 paused 设置")
		}
		s.Registry.SetPaused(*in.Paused)
		if s.Chrome != nil {
			s.Chrome.Refresh()
		}
		return nil
	case "capability.set":
		var in struct {
			Category string `json:"category"`
			Enabled  *bool  `json:"enabled"`
		}
		if err := json.Unmarshal(cmd.Payload, &in); err != nil {
			return err
		}
		if in.Enabled == nil {
			return errors.New("缺少 enabled 设置")
		}
		if !capabilityCategory(in.Category) {
			return errors.New("未知的能力配置")
		}
		return s.setCapability(in.Category, *in.Enabled)
	default:
		return errors.New("不支持的云端命令")
	}
}

// RelayConsentRequired is returned when relay mode is requested without acknowledging
// that tool data passes through the cloud service.
const RelayConsentRequired = "开启云端转发前，请确认数据将经 ReadyRig 云端服务器转发"

func (s *Server) cloudStatus() any {
	if s.Cloud == nil {
		return nil
	}
	return s.Cloud.Status()
}
func (s *Server) cloudRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cloud", func(w http.ResponseWriter, r *http.Request) { write(w, s.cloudStatus()) })
	mux.HandleFunc("POST /api/cloud/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			URL  string `json:"url"`
			Name string `json:"name"`
		}
		if !decode(w, r, &in) {
			return
		}
		if s.Cloud == nil {
			problem(w, 409, errors.New("云端连接尚未初始化"))
			return
		}
		status, err := s.Cloud.Login(in.URL, in.Name)
		if err != nil {
			problem(w, 400, err)
			return
		}
		write(w, status)
	})
	// Relay mode sends tool arguments and results through the cloud service instead of a
	// Cloudflare tunnel. It is off by default and can only be turned on here, after the
	// person at this computer has acknowledged that.
	mux.HandleFunc("POST /api/cloud/relay", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enabled      *bool `json:"enabled"`
			Acknowledged bool  `json:"acknowledged"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Enabled == nil {
			problem(w, 400, errors.New("缺少 enabled 设置"))
			return
		}
		if s.Cloud == nil {
			problem(w, 409, errors.New("云端连接尚未初始化"))
			return
		}
		if *in.Enabled && !in.Acknowledged {
			problem(w, 400, errors.New(RelayConsentRequired))
			return
		}
		if err := s.Cloud.SetRelay(*in.Enabled); err != nil {
			problem(w, 409, err)
			return
		}
		write(w, s.cloudStatus())
	})
	mux.HandleFunc("POST /api/cloud/disconnect", func(w http.ResponseWriter, r *http.Request) {
		if s.Cloud != nil {
			if err := s.Cloud.Disconnect(); err != nil {
				problem(w, 502, err)
				return
			}
		}
		write(w, s.cloudStatus())
	})
}
