package cloud

import (
	"context"
	"encoding/json"
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

// testHub plays the cloud service: heartbeats succeed and /api/agent/relay accepts
// a WebSocket from a device holding the right credential.
type testHub struct {
	server   *httptest.Server
	connects atomic.Int32
	auths    chan string
	conns    chan *websocket.Conn
	status   atomic.Int32 // optional HTTP status to refuse the upgrade with
}

func newTestHub(t *testing.T) *testHub {
	h := &testHub{auths: make(chan string, 16), conns: make(chan *websocket.Conn, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agent/heartbeat", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"command":null}`)) })
	mux.HandleFunc("/api/agent/relay", func(w http.ResponseWriter, r *http.Request) {
		h.connects.Add(1)
		h.auths <- r.Header.Get("Authorization")
		if code := int(h.status.Load()); code != 0 {
			w.WriteHeader(code)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.SetReadLimit(relayReadLimit)
		h.conns <- conn
	})
	h.server = httptest.NewServer(mux)
	t.Cleanup(h.server.Close)
	return h
}

func (h *testHub) accept(t *testing.T) *websocket.Conn {
	t.Helper()
	select {
	case conn := <-h.conns:
		t.Cleanup(func() { _ = conn.CloseNow() })
		return conn
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not connect")
		return nil
	}
}

func send(t *testing.T, conn *websocket.Conn, frame relayFrame) {
	t.Helper()
	data, _ := json.Marshal(frame)
	if err := conn.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

// result reads frames until it sees a result, skipping the client's keep-alive pings.
func result(t *testing.T, conn *websocket.Conn) relayFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var frame relayFrame
		if json.Unmarshal(data, &frame) == nil && frame.Type == "result" {
			return frame
		}
	}
}

func waitRelay(t *testing.T, c *Client, state string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.Relay().State != state && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if c.Relay().State != state {
		t.Fatalf("relay state %q, want %q", c.Relay().State, state)
	}
}

func boundClient(t *testing.T, hub *testHub, opts Options) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	if err := save(dir, "cloud.json", credentials{URL: hub.server.URL, Token: "private-device-token", DeviceID: "device"}); err != nil {
		t.Fatal(err)
	}
	opts.Dir, opts.Interval = dir, 20*time.Millisecond
	c := New(opts)
	t.Cleanup(c.Close)
	return c, dir
}

func TestRelayIsOffUntilTheUserTurnsItOn(t *testing.T) {
	hub := newTestHub(t)
	c, dir := boundClient(t, hub, Options{Relay: func(context.Context, RelayCall) RelayReply {
		return RelayReply{Status: 200, Body: json.RawMessage(`{}`)}
	}})
	if got := c.Relay(); got.Enabled || got.State != "off" {
		t.Fatal(got)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if hub.connects.Load() != 0 {
		t.Fatal("relay connected without being turned on")
	}
	if _, err := os.Stat(filepath.Join(dir, relayConfigFile)); !os.IsNotExist(err) {
		t.Fatal("relay setting written by default", err)
	}
	// Not bound to an account: it cannot be turned on at all.
	unbound := New(Options{Dir: t.TempDir()})
	if err := unbound.SetRelay(true); err == nil || unbound.Relay().Enabled {
		t.Fatal("relay enabled without an account", err)
	}
}

func TestRelayRunsCallsAndReturnsResults(t *testing.T) {
	hub := newTestHub(t)
	calls := make(chan RelayCall, 4)
	c, dir := boundClient(t, hub, Options{Relay: func(ctx context.Context, call RelayCall) RelayReply {
		calls <- call
		return RelayReply{Status: 423, Body: json.RawMessage(`{"error":"Capability disabled","status":"denied"}`)}
	}})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRelay(true); err != nil {
		t.Fatal(err)
	}
	conn := hub.accept(t)
	if auth := <-hub.auths; auth != "Bearer private-device-token" {
		t.Fatal("device credential not sent:", auth)
	}
	waitRelay(t, c, "connected")
	data, _ := os.ReadFile(filepath.Join(dir, relayConfigFile))
	if !strings.Contains(string(data), `"enabled":true`) {
		t.Fatal(string(data))
	}
	send(t, conn, relayFrame{Type: "call", ID: "one", Tool: "exec_command", Session: "cloud-grant", Client: "ReadyRig Cloud MCP", Arguments: json.RawMessage(`{"command":"pwd"}`)})
	got := <-calls
	if got.Tool != "exec_command" || got.Session != "cloud-grant" || got.Client != "ReadyRig Cloud MCP" || string(got.Arguments) != `{"command":"pwd"}` {
		t.Fatalf("%+v", got)
	}
	reply := result(t, conn)
	if reply.ID != "one" || reply.HTTPStatus != 423 || !strings.Contains(string(reply.Body), "Capability disabled") {
		t.Fatalf("%+v %s", reply, reply.Body)
	}
	// Calls that are not well-formed tool invocations never reach the tools.
	for i, frame := range []relayFrame{{ID: "a", Tool: "../api/logout"}, {ID: "b", Tool: "read_file", Session: strings.Repeat("s", 129)}, {ID: "c", Tool: ""}} {
		frame.Type = "call"
		send(t, conn, frame)
		if reply := result(t, conn); reply.ID != frame.ID || reply.HTTPStatus != 400 {
			t.Fatal(i, reply)
		}
	}
	select {
	case extra := <-calls:
		t.Fatalf("invalid call reached the tools: %+v", extra)
	default:
	}
}

func TestRelayCancelAndOversizedResults(t *testing.T) {
	hub := newTestHub(t)
	started, cancelled := make(chan struct{}), make(chan struct{})
	c, _ := boundClient(t, hub, Options{Relay: func(ctx context.Context, call RelayCall) RelayReply {
		switch call.Tool {
		case "slow":
			close(started)
			<-ctx.Done()
			close(cancelled)
			return RelayReply{Status: 422, Body: json.RawMessage(`{}`)}
		default:
			big, _ := json.Marshal(map[string]string{"data": strings.Repeat("x", relayResultLimit+1)})
			return RelayReply{Status: 200, Body: big}
		}
	}})
	_ = c.Start()
	_ = c.SetRelay(true)
	conn := hub.accept(t)
	waitRelay(t, c, "connected")
	send(t, conn, relayFrame{Type: "call", ID: "slow", Tool: "slow"})
	<-started
	send(t, conn, relayFrame{Type: "cancel", ID: "slow"})
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not reach the running tool")
	}
	send(t, conn, relayFrame{Type: "call", ID: "big", Tool: "huge"})
	reply := result(t, conn)
	if reply.ID != "big" || reply.HTTPStatus != 422 || !strings.Contains(string(reply.Body), "8 MiB") {
		t.Fatalf("%+v %.80s", reply, reply.Body)
	}
}

func TestRelayLimitsConcurrentCalls(t *testing.T) {
	hub := newTestHub(t)
	release := make(chan struct{})
	var running atomic.Int32
	c, _ := boundClient(t, hub, Options{Relay: func(ctx context.Context, call RelayCall) RelayReply {
		running.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return RelayReply{Status: 200, Body: json.RawMessage(`{}`)}
	}})
	_ = c.Start()
	_ = c.SetRelay(true)
	conn := hub.accept(t)
	waitRelay(t, c, "connected")
	for i := 0; i < relayMaxCalls; i++ {
		send(t, conn, relayFrame{Type: "call", ID: string(rune('a' + i)), Tool: "wait"})
	}
	deadline := time.Now().Add(5 * time.Second)
	for running.Load() < relayMaxCalls && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	send(t, conn, relayFrame{Type: "call", ID: "over", Tool: "wait"})
	if reply := result(t, conn); reply.ID != "over" || reply.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("%+v", reply)
	}
	close(release)
}

func TestRelayTurnsOffAndReconnects(t *testing.T) {
	hub := newTestHub(t)
	c, dir := boundClient(t, hub, Options{Relay: func(context.Context, RelayCall) RelayReply {
		return RelayReply{Status: 200, Body: json.RawMessage(`{}`)}
	}})
	_ = c.Start()
	_ = c.SetRelay(true)
	first := hub.accept(t)
	waitRelay(t, c, "connected")
	// A dropped connection is retried on its own.
	_ = first.Close(websocket.StatusInternalError, "drop")
	second := hub.accept(t)
	waitRelay(t, c, "connected")
	// Turning it off closes the socket, stays off and survives a restart.
	if err := c.SetRelay(false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := second.Read(ctx); err == nil {
		t.Fatal("socket stayed open after turning relay off")
	}
	waitRelay(t, c, "off")
	before := hub.connects.Load()
	time.Sleep(200 * time.Millisecond)
	if hub.connects.Load() != before {
		t.Fatal("relay reconnected while off")
	}
	if got := New(Options{Dir: dir}); got.Relay().Enabled {
		t.Fatal("relay re-enabled itself after a restart")
	}
	_ = c.SetRelay(true)
	hub.accept(t)
	waitRelay(t, c, "connected")
	c.Close()
	if got := New(Options{Dir: dir}); !got.Relay().Enabled {
		t.Fatal("relay choice was not remembered")
	}
}

func TestRelayStatusAndFailures(t *testing.T) {
	hub := newTestHub(t)
	hub.status.Store(404)
	c, _ := boundClient(t, hub, Options{Relay: func(context.Context, RelayCall) RelayReply {
		return RelayReply{Status: 200, Body: json.RawMessage(`{}`)}
	}})
	_ = c.Start()
	_ = c.SetRelay(true)
	waitRelay(t, c, "error")
	if msg := c.Relay().Message; !strings.Contains(msg, "未开启云端转发") {
		t.Fatal(msg)
	}
	if c.Status().Relay.State != "error" {
		t.Fatal("relay state missing from the account status", c.Status())
	}
	// A revoked credential is reported without retrying in a loop.
	hub.status.Store(401)
	_ = c.SetRelay(false)
	_ = c.SetRelay(true)
	waitRelay(t, c, "error")
	if !strings.Contains(c.Relay().Message, "失效") {
		t.Fatal(c.Relay().Message)
	}
}

func TestDisconnectForgetsRelayConsentAndNeverSendsCredentialsOnRedirect(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(r.Header.Get("Authorization") != "") }))
	defer target.Close()
	var mu sync.Mutex
	var disconnected bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent/relay":
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		case "/api/agent/disconnect":
			mu.Lock()
			disconnected = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{"command":null}`))
		}
	}))
	defer api.Close()
	dir := t.TempDir()
	_ = save(dir, "cloud.json", credentials{URL: api.URL, Token: "private-device-token", DeviceID: "device"})
	c := New(Options{Dir: dir, Interval: 20 * time.Millisecond, Relay: func(context.Context, RelayCall) RelayReply { return RelayReply{} }})
	_ = c.Start()
	_ = c.SetRelay(true)
	waitRelay(t, c, "error")
	if reached.Load() {
		t.Fatal("the device credential followed a redirect")
	}
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !disconnected {
		t.Fatal("account was not disconnected")
	}
	if _, err := os.Stat(filepath.Join(dir, relayConfigFile)); !os.IsNotExist(err) {
		t.Fatal("relay consent kept after disconnecting the account", err)
	}
	if got := c.Relay(); got.Enabled || got.State != "off" {
		t.Fatal(got)
	}
}

// A relay.stop command runs inside the heartbeat loop. Disconnect waits for that loop while
// holding the lifecycle lock, so SetRelay must not need the same lock.
func TestDisconnectDoesNotDeadlockWithARunningRelayStopCommand(t *testing.T) {
	var delivered atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/heartbeat" && delivered.CompareAndSwap(false, true) {
			_ = json.NewEncoder(w).Encode(map[string]any{"command": Command{ID: "stop", Kind: "relay.stop", Payload: json.RawMessage(`{}`)}})
			return
		}
		_, _ = w.Write([]byte(`{"command":null}`))
	}))
	defer api.Close()
	dir := t.TempDir()
	_ = save(dir, "cloud.json", credentials{URL: api.URL, Token: "private-device-token", DeviceID: "device"})
	entered, release := make(chan struct{}), make(chan struct{})
	var c *Client
	c = New(Options{Dir: dir, Interval: 20 * time.Millisecond, Execute: func(cmd Command) error {
		close(entered)
		<-release
		return c.SetRelay(false)
	}})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	<-entered
	done := make(chan error, 1)
	go func() { done <- c.Disconnect() }()
	time.Sleep(100 * time.Millisecond) // let Disconnect take the lifecycle lock and start waiting
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Disconnect deadlocked with a running relay.stop command")
	}
}

func TestRelayConnectsOnlyWhileTheTunnelIsNotWorking(t *testing.T) {
	old := relayStandbyDelay
	relayStandbyDelay = 300 * time.Millisecond
	t.Cleanup(func() { relayStandbyDelay = old })
	hub := newTestHub(t)
	var ready atomic.Bool
	ready.Store(true)
	started, release := make(chan struct{}), make(chan struct{})
	c, _ := boundClient(t, hub, Options{TunnelReady: ready.Load, Relay: func(ctx context.Context, call RelayCall) RelayReply {
		close(started)
		<-release
		return RelayReply{Status: 200, Body: json.RawMessage(`{"ok":true}`)}
	}})
	_ = c.Start()
	_ = c.SetRelay(true)
	// Tunnel working: relay is on but holds no connection.
	waitRelay(t, c, "standby")
	time.Sleep(300 * time.Millisecond)
	if hub.connects.Load() != 0 || c.Status().Relay.State != "standby" {
		t.Fatal("relay connected while the tunnel works", hub.connects.Load())
	}
	// Tunnel down: the relay takes over.
	ready.Store(false)
	conn := hub.accept(t)
	waitRelay(t, c, "connected")
	// Tunnel back, but a call is still running: the socket stays until that call is answered.
	send(t, conn, relayFrame{Type: "call", ID: "busy", Tool: "slow"})
	<-started
	ready.Store(true)
	time.Sleep(900 * time.Millisecond)
	if c.Relay().State != "connected" {
		t.Fatal("a running relayed call was cut off", c.Relay())
	}
	close(release)
	if reply := result(t, conn); reply.ID != "busy" || reply.HTTPStatus != 200 {
		t.Fatalf("%+v", reply)
	}
	// Then it steps back to standby and closes the socket.
	waitRelay(t, c, "standby")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("socket stayed open on standby")
	}
	// A tunnel that fails again brings the relay back.
	ready.Store(false)
	hub.accept(t)
	waitRelay(t, c, "connected")
}
