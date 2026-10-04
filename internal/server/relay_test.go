package server

import (
	"computer-use-server/internal/cloud"
	"computer-use-server/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func relayState(t *testing.T, s *Server) cloud.Status {
	t.Helper()
	w := request(s.UI(), "GET", "/api/cloud", "", "")
	var status cloud.Status
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err, w.Body.String())
	}
	return status
}

func TestRelayNeedsLocalConsentAndAnAccountAndIsNeverPublic(t *testing.T) {
	s := fixture(t)
	if err := s.StartCloud(""); err != nil {
		t.Fatal(err)
	}
	defer s.Cloud.Close()
	if status := relayState(t, s); status.Relay.Enabled || status.Relay.State != "off" {
		t.Fatal("relay must default to off", status.Relay)
	}
	// Turning it on without acknowledging the data transfer is refused.
	w := request(s.UI(), "POST", "/api/cloud/relay", `{"enabled":true}`, "")
	if w.Code != 400 || !strings.Contains(w.Body.String(), RelayConsentRequired) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []string{`{"enabled":true,"acknowledged":false}`, `{"acknowledged":true}`, `{}`} {
		if w := request(s.UI(), "POST", "/api/cloud/relay", body, ""); w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	// Acknowledged, but there is no bound account to relay through.
	if w := request(s.UI(), "POST", "/api/cloud/relay", `{"enabled":true,"acknowledged":true}`, ""); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if status := relayState(t, s); status.Relay.Enabled {
		t.Fatal("relay enabled without an account")
	}
	// Turning it off never needs acknowledgement.
	if w := request(s.UI(), "POST", "/api/cloud/relay", `{"enabled":false}`, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// The public gateway cannot read or change it, nor can it open its own relay.
	if w := request(s.Gateway(), "POST", "/app/api/cloud/relay", `{"enabled":true,"acknowledged":true}`, s.AccessPath); w.Code != http.StatusForbidden {
		t.Fatal("relay setting reachable from the gateway", w.Code)
	}
	if w := request(s.Gateway(), "GET", "/api/cloud/relay", "", s.AccessPath); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestBoundAccountCanEnableRelayLocallyAndStopItFromTheCloud(t *testing.T) {
	cloudAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/relay" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"command":null}`))
	}))
	defer cloudAPI.Close()
	s := fixture(t)
	dir := filepath.Join(s.Store.Dir, "cloud")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	creds, _ := json.Marshal(map[string]string{"url": cloudAPI.URL, "device_id": "device", "token": "private-device-token", "name": "Test"})
	if err := os.WriteFile(filepath.Join(dir, "cloud.json"), creds, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.StartCloud(""); err != nil {
		t.Fatal(err)
	}
	defer s.Cloud.Close()
	if w := request(s.UI(), "POST", "/api/cloud/relay", `{"enabled":true,"acknowledged":true}`, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !relayState(t, s).Relay.Enabled {
		t.Fatal("relay not enabled after consent")
	}
	// The heartbeat reports only the relay state, never connection details or secrets.
	data, _ := json.Marshal(s.cloudSnapshot())
	var snapshot struct {
		Relay map[string]string `json:"relay"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil || len(snapshot.Relay) != 2 || strings.Contains(string(data), "private-device-token") {
		t.Fatal(err, string(data))
	}
	// The cloud can switch relay off but has no command that switches it on.
	if err := s.executeCloudCommand(cloud.Command{ID: "1", Kind: "relay.start", Payload: json.RawMessage(`{"enabled":true}`)}); err == nil {
		t.Fatal("cloud command enabled relay")
	}
	if !relayState(t, s).Relay.Enabled {
		t.Fatal("rejected command changed relay")
	}
	if err := s.executeCloudCommand(cloud.Command{ID: "2", Kind: "relay.stop", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if status := relayState(t, s); status.Relay.Enabled || status.Relay.State != "off" {
		t.Fatal(status.Relay)
	}
}

func TestRelayCallsUseTheGatewayChecksAndLogging(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	call := func(tool, args string) (int, map[string]any) {
		reply := s.relayCall(ctx, cloud.RelayCall{Tool: tool, Session: "cloud-test", Client: "ReadyRig Cloud MCP", Arguments: json.RawMessage(args)})
		var body map[string]any
		if err := json.Unmarshal(reply.Body, &body); err != nil {
			t.Fatal(err, string(reply.Body))
		}
		return reply.Status, body
	}
	// Same status and result as the same call through the gateway.
	status, body := call("list_directory", `{"path":"."}`)
	viaGateway := request(s.Gateway(), "POST", "/api/v1/tools/list_directory", `{"path":"."}`, s.AccessPath)
	if status != viaGateway.Code || status != 200 || body["status"] != "success" {
		t.Fatal(status, viaGateway.Code, body)
	}
	if status, body = call("list_directory", ``); status == 0 || body["status"] == nil {
		t.Fatal("empty arguments are treated as {}", status, body)
	}
	if status, body = call("no_such_tool", `{}`); status == 200 || body["error"] == "" {
		t.Fatal(status, body)
	}
	// Pause and capability switches apply exactly as they do for direct calls.
	s.Registry.SetPaused(true)
	if status, body = call("list_directory", `{"path":"."}`); status != 423 || body["status"] != "denied" {
		t.Fatal("paused computer ran a relayed tool", status, body)
	}
	s.Registry.SetPaused(false)
	if err := s.Registry.Enable("files", false); err != nil {
		t.Fatal(err)
	}
	if status, body = call("list_directory", `{"path":"."}`); status != 423 {
		t.Fatal("disabled capability ran a relayed tool", status, body)
	}
	_ = s.Registry.Enable("files", true)
	if reply := s.relayCall(ctx, cloud.RelayCall{Tool: "list_directory", Arguments: json.RawMessage(`{"path":"` + strings.Repeat("a", maxRelayArguments) + `"}`)}); reply.Status != http.StatusRequestEntityTooLarge {
		t.Fatal("oversized arguments accepted", reply.Status)
	}
	// Relayed calls are logged locally under the cloud session, like any other call.
	deadline := time.Now().Add(2 * time.Second)
	for {
		calls, total, err := s.Store.List(store.Filter{Session: "cloud-test", Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if total >= 3 && calls[0].Session == "cloud-test" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("relayed calls missing from the local log", total)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
