package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloudURLs(t *testing.T) {
	for _, raw := range []string{"https://console.example", "http://127.0.0.1:8787", "http://[::1]:8787", "http://localhost:8787/"} {
		if _, err := ValidateURL(raw); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{"http://console.example", "https://u:p@console.example", "https://console.example/path", "https://console.example?token=x", "ftp://localhost", "https://console.example/#fragment"} {
		if _, err := ValidateURL(raw); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}
func TestHeartbeatExecutesOncePersistsAndResendsReceipts(t *testing.T) {
	var calls atomic.Int32
	var requests []struct {
		Results []Result `json:"results"`
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-device-token" {
			t.Error("missing device credential")
		}
		var body struct {
			Results []Result `json:"results"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, body)
		if len(requests) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "Renamed Mac", "command": Command{ID: "cmd-1", Kind: "control.pause", Payload: json.RawMessage(`{"paused":true}`)}})
			return
		}
		if len(requests) == 2 {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"command": nil})
	}))
	defer api.Close()
	c := New(Options{Dir: t.TempDir(), Snapshot: func() any { return map[string]any{"paused": false} }, Execute: func(cmd Command) error { calls.Add(1); return errors.New("test failure") }})
	creds := credentials{URL: api.URL, Token: "private-device-token"}
	if err := c.heartbeat(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	if c.Status().Name != "Renamed Mac" {
		t.Fatal(c.Status())
	}
	if err := c.heartbeat(context.Background(), creds); err == nil {
		t.Fatal("network failure ignored")
	}
	if err := c.heartbeat(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(requests[1].Results) != 1 || len(requests[2].Results) != 1 || requests[2].Results[0].Error != "test failure" {
		t.Fatal(calls.Load(), requests)
	}
	if len(c.results) != 0 {
		t.Fatal("receipt not acknowledged")
	}
	data, err := os.ReadFile(filepath.Join(c.opts.Dir, "cloud-results.json"))
	if err != nil || string(data) != "[]" {
		t.Fatal(string(data), err)
	}
}
func TestRestartLoadsCrashReceiptAndProtectsSavedOrigin(t *testing.T) {
	dir := t.TempDir()
	if err := save(dir, "cloud.json", credentials{URL: "https://original.example", Token: "secret", DeviceID: "device"}); err != nil {
		t.Fatal(err)
	}
	if err := save(dir, "cloud-results.json", []Result{{ID: "interrupted", Error: "unknown outcome"}}); err != nil {
		t.Fatal(err)
	}
	c := New(Options{Dir: dir, URL: "https://different.example"})
	if c.creds.URL != "https://original.example" || len(c.results) != 1 {
		t.Fatal(c.creds.URL, c.results)
	}
	info, err := os.Stat(filepath.Join(dir, "cloud.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	data, _ := json.Marshal(c.Status())
	if strings.Contains(string(data), "secret") {
		t.Fatal("credential leaked into status")
	}
}
func TestCredentialsNeverFollowRedirects(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	c := New(Options{Dir: t.TempDir()})
	if err := c.request(context.Background(), source.URL, "/api/agent/heartbeat", "private", map[string]any{}, nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if reached.Load() {
		t.Fatal("redirect forwarded credential")
	}
}
func TestRevokedCredentialsStopHeartbeatAndDisconnectLocally(t *testing.T) {
	var requests atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(401) }))
	defer api.Close()
	dir := t.TempDir()
	_ = save(dir, "cloud.json", credentials{URL: api.URL, Token: "secret", DeviceID: "device"})
	c := New(Options{Dir: dir, Interval: time.Millisecond})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for c.Status().State != "revoked" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.Status().State != "revoked" {
		t.Fatal(c.Status())
	}
	c.Close()
	if requests.Load() != 1 {
		t.Fatal("revoked credentials retried", requests.Load())
	}
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cloud.json")); !os.IsNotExist(err) {
		t.Fatal("credential retained", err)
	}
}
func TestRenameUpdatesCloudAndSavedCredentials(t *testing.T) {
	var got string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/rename" || r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(404)
			return
		}
		var in struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		got = in.Name
		_, _ = w.Write([]byte(`{}`))
	}))
	defer api.Close()
	dir := t.TempDir()
	_ = save(dir, "cloud.json", credentials{URL: api.URL, Token: "secret", DeviceID: "device", Name: "old"})
	c := New(Options{Dir: dir})
	if _, err := c.Rename("   "); err == nil {
		t.Fatal("accepted blank name")
	}
	status, err := c.Rename(" Studio Mac ")
	if err != nil || status.Name != "Studio Mac" || got != "Studio Mac" {
		t.Fatal(status, err, got)
	}
	if New(Options{Dir: dir}).Status().Name != "Studio Mac" {
		t.Fatal("rename not saved")
	}
	_ = save(dir, "cloud.json", credentials{URL: api.URL + "/missing", Token: "secret", DeviceID: "device", Name: "old"})
	if _, err := New(Options{Dir: dir}).Rename("new"); err == nil || !strings.Contains(err.Error(), "网页控制台") {
		t.Fatal("old cloud error", err)
	}
}
func TestNameSyncsBothWays(t *testing.T) {
	var cloudName atomic.Value
	cloudName.Store("old")
	var online atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !online.Load() {
			panic(http.ErrAbortHandler) // drop the connection like an unreachable cloud
		}
		var in struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Name != "" {
			cloudName.Store(in.Name)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": cloudName.Load()})
	}))
	defer api.Close()
	dir := t.TempDir()
	_ = save(dir, "cloud.json", credentials{URL: api.URL, Token: "secret", DeviceID: "device", Name: "old"})
	c := New(Options{Dir: dir})
	status, err := c.Rename("offline name")
	if err != nil || !status.NamePending || status.Name != "offline name" {
		t.Fatal("offline rename", status, err)
	}
	if !New(Options{Dir: dir}).Status().NamePending {
		t.Fatal("pending rename not saved")
	}
	online.Store(true)
	if err := c.heartbeat(context.Background(), c.creds); err != nil {
		t.Fatal(err)
	}
	if cloudName.Load() != "offline name" || c.Status().NamePending {
		t.Fatal("pending rename not delivered", cloudName.Load(), c.Status())
	}
	cloudName.Store("from web")
	if err := c.heartbeat(context.Background(), c.creds); err != nil {
		t.Fatal(err)
	}
	if c.Status().Name != "from web" || New(Options{Dir: dir}).Status().Name != "from web" {
		t.Fatal("web rename not applied and saved", c.Status())
	}
}
