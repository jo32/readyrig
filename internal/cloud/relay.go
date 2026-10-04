package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Relay mode is an opt-in alternative to a Cloudflare tunnel. The computer opens one
// outbound WebSocket to its cloud site and answers tool calls sent over it. Unlike a
// tunnel, tool arguments and results (file contents, command output, screenshots)
// pass through the cloud service, so it is never enabled by default and can only be
// turned on from this computer.
const (
	relayReadLimit    = 16 << 20
	relayResultLimit  = 8 << 20
	relayMaxCalls     = 8
	relayPingEvery    = 25 * time.Second
	relayIdleTimeout  = 80 * time.Second
	relayMaxBackoff   = time.Minute
	relayConfigFile   = "relay.json"
	relayStateOff     = "off"
	relayStateStandby = "standby" // on, but the tunnel works, so no socket is open
	relayStateConnect = "connecting"
	relayStateReady   = "connected"
	relayStateError   = "error"
)

// ErrNoAccount is returned when relay mode is requested before a Google account is bound.
var ErrNoAccount = errors.New("请先登录 Google 账号并绑定这台电脑")

// Bound reports whether this computer is linked to a cloud account.
func (c *Client) Bound() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.creds.Token != ""
}

// relayStandbyDelay is how long the tunnel must stay ready before an open relay socket is
// closed, so a flapping tunnel does not make the relay flap with it.
var relayStandbyDelay = 5 * time.Second

func (c *Client) tunnelReady() bool { return c.opts.TunnelReady != nil && c.opts.TunnelReady() }

var relayToolName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)

// RelayStatus is shown locally and, without the message detail, sent with each heartbeat.
type RelayStatus struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	Message string `json:"message"`
}

// RelayCall is a tool invocation received from the cloud.
type RelayCall struct {
	Tool      string
	Session   string
	Client    string
	Arguments json.RawMessage
}

// RelayReply is the HTTP-shaped response of the local tool gateway.
type RelayReply struct {
	Status int
	Body   json.RawMessage
}

type relayConfig struct {
	Enabled bool `json:"enabled"`
}

type relayFrame struct {
	Type       string          `json:"type"`
	ID         string          `json:"id,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Session    string          `json:"session,omitempty"`
	Client     string          `json:"client,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	HTTPStatus int             `json:"http_status,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
}

func (c *Client) loadRelay() {
	var cfg relayConfig
	if data, err := os.ReadFile(filepath.Join(c.opts.Dir, relayConfigFile)); err == nil && json.Unmarshal(data, &cfg) == nil {
		c.relay.Enabled = cfg.Enabled
	}
	c.relay.State = relayStateOff
}

// Relay reports whether relay mode is on and whether its socket is connected.
func (c *Client) Relay() RelayStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.relay
}

func (c *Client) setRelay(state, message string) {
	c.mu.Lock()
	changed := c.relay.State != state || c.relay.Message != message
	c.relay.State, c.relay.Message = state, message
	c.status.Relay = c.relay
	c.mu.Unlock()
	if changed {
		c.changed()
	}
}

// SetRelay turns relay mode on or off and remembers the choice. Turning it on needs a
// bound account. The caller is responsible for having obtained the user's consent.
func (c *Client) SetRelay(enabled bool) error {
	// Not the lifecycle lock: a relay.stop command runs inside the heartbeat loop, which
	// Disconnect waits for while holding that lock.
	c.relayMu.Lock()
	defer c.relayMu.Unlock()
	c.mu.Lock()
	if enabled && c.creds.Token == "" {
		c.mu.Unlock()
		return ErrNoAccount
	}
	previous := c.relay.Enabled
	c.mu.Unlock()
	if err := save(c.opts.Dir, relayConfigFile, relayConfig{Enabled: enabled}); err != nil {
		return err
	}
	c.mu.Lock()
	c.relay.Enabled = enabled
	if !enabled {
		c.relay.State, c.relay.Message = relayStateOff, ""
	}
	c.status.Relay = c.relay
	cancel := c.relayCancel
	c.mu.Unlock()
	if !enabled && cancel != nil {
		cancel()
	}
	if previous != enabled {
		select {
		case c.relayWake <- struct{}{}:
		default:
		}
	}
	c.changed()
	return nil
}

func (c *Client) relayWanted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.relay.Enabled
}

func (c *Client) relayURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/api/agent/relay"
	return u.String(), nil
}

func waitOrDone(ctx context.Context, wake <-chan struct{}, d time.Duration) {
	var timer <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-ctx.Done():
	case <-wake:
	case <-timer:
	}
}

// relayLoop keeps the relay socket connected while relay mode is on. It ends with ctx
// (sign-out, revocation or shutdown).
func (c *Client) relayLoop(ctx context.Context, creds credentials) {
	backoff := time.Second
	for ctx.Err() == nil {
		if !c.relayWanted() {
			c.setRelay(relayStateOff, "")
			waitOrDone(ctx, c.relayWake, 0)
			backoff = time.Second
			continue
		}
		// The relay is a fallback: while the tunnel works there is nothing to relay, so no
		// connection is held open and no tool data can reach the cloud through it.
		if c.tunnelReady() {
			c.setRelay(relayStateStandby, "待命 · 直连链接正常，云端转发未连接")
			waitOrDone(ctx, c.relayWake, time.Second)
			backoff = time.Second
			continue
		}
		started := time.Now()
		err := c.relaySession(ctx, creds)
		if ctx.Err() != nil {
			return
		}
		if !c.relayWanted() {
			continue
		}
		if errors.Is(err, errUnauthorized) {
			c.setRelay(relayStateError, "设备凭证已失效，无法使用云端转发")
			waitOrDone(ctx, c.relayWake, 0)
			continue
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second
		}
		message := "云端转发连接中断，将自动重试"
		if errors.Is(err, errRelayUnsupported) {
			message = "此云端服务未开启云端转发"
			backoff = relayMaxBackoff
		}
		c.setRelay(relayStateError, message)
		waitOrDone(ctx, c.relayWake, backoff)
		if backoff *= 2; backoff > relayMaxBackoff {
			backoff = relayMaxBackoff
		}
	}
}

var errRelayUnsupported = errors.New("relay is not available on this cloud service")

// relaySession runs one WebSocket connection until it ends.
func (c *Client) relaySession(ctx context.Context, creds credentials) error {
	endpoint, err := c.relayURL(creds.URL)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	c.relayCancel = cancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.relayCancel = nil
		c.mu.Unlock()
	}()
	c.setRelay(relayStateConnect, "正在连接云端转发")
	// The WebSocket must outlive any request timeout, and the device credential must
	// never follow a redirect.
	httpClient := &http.Client{Transport: c.opts.Client.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	dialCtx, dialCancel := context.WithTimeout(sctx, 15*time.Second)
	conn, resp, err := websocket.Dial(dialCtx, endpoint, &websocket.DialOptions{HTTPClient: httpClient, HTTPHeader: http.Header{"Authorization": {"Bearer " + creds.Token}}})
	dialCancel()
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				return errUnauthorized
			case http.StatusNotFound, http.StatusServiceUnavailable, http.StatusNotImplemented:
				return errRelayUnsupported
			}
		}
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(relayReadLimit)
	c.setRelay(relayStateReady, "已连接 · 工具数据经云端转发")

	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())
	go func() {
		tick := time.NewTicker(relayPingEvery)
		defer tick.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-tick.C:
				if time.Since(time.Unix(0, lastRead.Load())) > relayIdleTimeout {
					_ = conn.Close(websocket.StatusGoingAway, "idle")
					cancel()
					return
				}
				// The cloud answers this text frame with "pong" without waking its relay.
				if conn.Write(sctx, websocket.MessageText, []byte("ping")) != nil {
					return
				}
			}
		}
	}()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		calls    = map[string]context.CancelFunc{}
		slots    = make(chan struct{}, relayMaxCalls)
		disabled = c.opts.Relay == nil
	)
	defer wg.Wait()
	defer cancel()
	// Once the tunnel has been ready for a while and no call is running, step back to standby.
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		var since time.Time
		for {
			select {
			case <-sctx.Done():
				return
			case <-tick.C:
			}
			if !c.tunnelReady() {
				since = time.Time{}
				continue
			}
			if since.IsZero() {
				since = time.Now()
			}
			if time.Since(since) >= relayStandbyDelay && len(slots) == 0 {
				cancel()
				return
			}
		}
	}()
	reply := func(id string, status int, body json.RawMessage) {
		if len(body) > relayResultLimit {
			status, body = http.StatusUnprocessableEntity, json.RawMessage(`{"error":"result exceeds 8 MiB; request a smaller result","status":"error"}`)
		}
		data, err := json.Marshal(relayFrame{Type: "result", ID: id, HTTPStatus: status, Body: body})
		if err == nil {
			_ = conn.Write(sctx, websocket.MessageText, data)
		}
	}
	fail := func(id string, status int, message string) {
		body, _ := json.Marshal(map[string]string{"error": message, "status": "error"})
		reply(id, status, body)
	}
	for {
		_, data, err := conn.Read(sctx)
		if err != nil {
			if sctx.Err() != nil {
				return nil
			}
			return err
		}
		lastRead.Store(time.Now().UnixNano())
		var frame relayFrame
		if json.Unmarshal(data, &frame) != nil {
			continue // "pong" and anything unrecognised
		}
		switch frame.Type {
		case "cancel":
			mu.Lock()
			if stop := calls[frame.ID]; stop != nil {
				stop()
			}
			mu.Unlock()
		case "call":
			if frame.ID == "" || len(frame.ID) > 64 {
				continue
			}
			if disabled || !relayToolName.MatchString(frame.Tool) || len(frame.Session) > 128 || len(frame.Client) > 128 {
				fail(frame.ID, http.StatusBadRequest, "invalid relay call")
				continue
			}
			select {
			case slots <- struct{}{}:
			default:
				fail(frame.ID, http.StatusTooManyRequests, "too many concurrent relay calls")
				continue
			}
			callCtx, stop := context.WithCancel(sctx)
			mu.Lock()
			calls[frame.ID] = stop
			mu.Unlock()
			wg.Add(1)
			go func(f relayFrame) {
				defer wg.Done()
				defer func() {
					stop()
					mu.Lock()
					delete(calls, f.ID)
					mu.Unlock()
					<-slots
				}()
				out := c.opts.Relay(callCtx, RelayCall{Tool: f.Tool, Session: f.Session, Client: f.Client, Arguments: f.Arguments})
				if callCtx.Err() != nil {
					return // cancelled or disconnected: nobody is waiting for the result
				}
				reply(f.ID, out.Status, out.Body)
			}(frame)
		}
	}
}
