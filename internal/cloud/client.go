// Package cloud connects a locally approved computer to its owner's cloud console.
package cloud

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Command struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}
type Result struct {
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
}
type Status struct {
	URL           string      `json:"url"`
	DeviceID      string      `json:"device_id,omitempty"`
	Name          string      `json:"name"`
	Email         string      `json:"email,omitempty"`
	State         string      `json:"state"`
	Message       string      `json:"message"`
	LastHeartbeat time.Time   `json:"last_heartbeat"`
	LoginURL      string      `json:"login_url,omitempty"`
	Code          string      `json:"code,omitempty"`
	Relay         RelayStatus `json:"relay"`
	// NamePending means a local rename is saved but has not reached the cloud yet.
	NamePending bool `json:"name_pending,omitempty"`
}
type credentials struct {
	URL      string `json:"url"`
	DeviceID string `json:"device_id"`
	Token    string `json:"token"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	// PendingName is a rename made while the cloud was unreachable; heartbeats
	// deliver it once the computer is back online.
	PendingName string `json:"pending_name,omitempty"`
}
type Options struct {
	Dir           string
	URL           string
	Client        *http.Client
	Snapshot      func() any
	Execute       func(Command) error
	Changed       func()
	RedactSecrets func(...string)
	// Relay runs one tool call received over the opt-in relay socket. Without it the
	// relay answers every call with an error.
	Relay func(context.Context, RelayCall) RelayReply
	// TunnelReady reports whether public sharing has a working link. The relay stays on
	// standby, without a connection, while it does.
	TunnelReady func() bool
	Interval    time.Duration
}
type Client struct {
	lifecycle   sync.Mutex
	mu          sync.Mutex
	opts        Options
	creds       credentials
	status      Status
	cancel      context.CancelFunc
	done        chan struct{}
	results     []Result
	relayMu     sync.Mutex // serializes SetRelay
	relay       RelayStatus
	relayCancel context.CancelFunc
	relayWake   chan struct{}
	nameRev     int // bumped by Rename so an in-flight heartbeat cannot restore the old name
}

func New(opts Options) *Client {
	if opts.Interval == 0 {
		opts.Interval = 15 * time.Second
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 12 * time.Second}
	}
	// Credentials must never be forwarded to redirects.
	copyClient := *opts.Client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	opts.Client = &copyClient
	name, _ := os.Hostname()
	c := &Client{opts: opts, creds: credentials{URL: opts.URL, Name: name}, relayWake: make(chan struct{}, 1)}
	if data, err := os.ReadFile(filepath.Join(opts.Dir, "cloud.json")); err == nil {
		if err := json.Unmarshal(data, &c.creds); err != nil {
			c.creds = credentials{URL: opts.URL, Name: name}
		}
	}
	if opts.URL != "" {
		// An endpoint override cannot redirect a saved credential to a new host.
		if c.creds.Token == "" {
			c.creds.URL = opts.URL
		}
	}
	c.status = Status{URL: c.creds.URL, Name: c.creds.Name, State: "signed_out", Message: "登录 Google，将这台电脑连接到网页", NamePending: c.creds.PendingName != ""}
	c.loadRelay()
	if c.creds.Token == "" {
		// Relay consent belongs to a bound account; never carry it over without one.
		c.relay = RelayStatus{State: relayStateOff}
	}
	c.status.Relay = c.relay
	if c.creds.Token != "" {
		if opts.RedactSecrets != nil {
			opts.RedactSecrets(c.creds.Token)
		}
		if data, err := os.ReadFile(filepath.Join(opts.Dir, "cloud-results.json")); err == nil {
			_ = json.Unmarshal(data, &c.results)
		}
	}
	return c
}
func ValidateURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return "", errors.New("云端地址必须是 HTTPS 网站地址")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", errors.New("云端地址必须使用 HTTPS；本机开发可用 HTTP")
	}
	return strings.TrimRight(u.String(), "/"), nil
}
func (c *Client) Status() Status { c.mu.Lock(); defer c.mu.Unlock(); return c.status }
func (c *Client) changed() {
	if c.opts.Changed != nil {
		c.opts.Changed()
	}
}
func (c *Client) setState(state, message string) {
	c.mu.Lock()
	c.status.State, c.status.Message = state, message
	c.mu.Unlock()
	c.changed()
}
func (c *Client) Start() error {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	return c.start()
}
func (c *Client) start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.creds.Token == "" {
		return nil
	}
	u, err := ValidateURL(c.creds.URL)
	if err != nil {
		return err
	}
	c.creds.URL = u
	if c.cancel != nil {
		return nil
	}
	c.status.DeviceID, c.status.Email = c.creds.DeviceID, c.creds.Email
	c.status.State, c.status.Message = "connecting", "正在连接云端"
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel, c.done = cancel, make(chan struct{})
	go c.run(ctx, c.done, c.creds)
	return nil
}
func (c *Client) stop() {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	c.mu.Lock()
	c.cancel, c.done = nil, nil
	c.mu.Unlock()
}
func (c *Client) Close() { c.lifecycle.Lock(); defer c.lifecycle.Unlock(); c.stop() }

// Login uses the system browser and a one-time pairing challenge. Only this
// computer knows the verifier; the browser never receives a device credential.
func (c *Client) Login(rawURL, name string) (Status, error) {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	c.mu.Lock()
	busy := c.cancel != nil
	c.mu.Unlock()
	if busy {
		return c.Status(), errors.New("请先断开当前账号，或等待登录完成")
	}
	u, err := ValidateURL(rawURL)
	if err != nil {
		return c.Status(), err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return c.Status(), errors.New("电脑名称需为 1–128 字节")
	}
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return c.Status(), err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	hash := sha256.Sum256([]byte(token))
	var pair struct {
		ID   string `json:"id"`
		Code string `json:"code"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err = c.request(ctx, u, "/api/agent/pair", "", map[string]any{"name": name, "platform": runtime.GOOS, "challenge": hex.EncodeToString(hash[:])}, &pair); err != nil {
		return c.Status(), err
	}
	if pair.ID == "" || strings.ContainsAny(pair.ID, "/?#&") {
		return c.Status(), errors.New("云端返回了无效登录请求")
	}
	c.mu.Lock()
	c.creds.URL, c.creds.Name = u, name
	c.status = Status{URL: u, Name: name, State: "signing_in", Message: "在浏览器登录 Google 并确认绑定，核对验证码", LoginURL: u + "/console?pair=" + url.QueryEscape(pair.ID), Code: pair.Code}
	ctx, stop := context.WithCancel(context.Background())
	c.cancel, c.done = stop, make(chan struct{})
	done := c.done
	c.mu.Unlock()
	c.changed()
	go c.pair(ctx, done, credentials{URL: u, Token: token, Name: name}, pair.ID)
	return c.Status(), nil
}
func (c *Client) pair(ctx context.Context, done chan struct{}, creds credentials, id string) {
	defer close(done)
	deadline := time.NewTimer(10 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			c.setState("error", "登录已过期，请断开后重新登录")
			return
		case <-tick.C:
			var reply struct {
				DeviceID string `json:"device_id"`
				Email    string `json:"email"`
			}
			if err := c.request(ctx, creds.URL, "/api/agent/pair/"+id, "", map[string]string{"verifier": creds.Token}, &reply); err != nil {
				c.setState("signing_in", "等待浏览器确认；如已确认，请检查网络")
				continue
			}
			if reply.DeviceID == "" {
				continue
			}
			creds.DeviceID, creds.Email = reply.DeviceID, reply.Email
			if err := save(c.opts.Dir, "cloud.json", creds); err != nil {
				c.setState("error", "无法保存设备凭证，请断开后重试")
				return
			}
			if c.opts.RedactSecrets != nil {
				c.opts.RedactSecrets(creds.Token)
			}
			c.mu.Lock()
			c.creds = creds
			c.status.DeviceID, c.status.Email, c.status.LoginURL, c.status.Code = creds.DeviceID, creds.Email, "", ""
			c.mu.Unlock()
			c.runLoop(ctx, creds)
			return
		}
	}
}

// Rename changes this computer's name in the cloud console. Before sign-in the
// name is only kept locally and sent with the next login. When the cloud cannot
// be reached the rename is saved and delivered by the next heartbeat.
func (c *Client) Rename(name string) (Status, error) {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return c.Status(), errors.New("电脑名称需为 1–128 字节")
	}
	c.mu.Lock()
	creds, pairing := c.creds, c.status.State == "signing_in"
	c.mu.Unlock()
	if pairing {
		return c.Status(), errors.New("请先断开当前账号，或等待登录完成")
	}
	if creds.Token != "" && creds.DeviceID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		err := c.request(ctx, creds.URL, "/api/agent/rename", creds.Token, map[string]string{"name": name}, nil)
		cancel()
		pending := ""
		if errors.Is(err, errOffline) {
			err, pending = nil, name
		}
		if err != nil && strings.Contains(err.Error(), "（404）") {
			err = errors.New("云端服务暂不支持在本机改名，请在网页控制台修改")
		}
		if err != nil {
			return c.Status(), err
		}
		creds.Name, creds.PendingName = name, pending
		if err := save(c.opts.Dir, "cloud.json", creds); err != nil {
			return c.Status(), err
		}
	}
	c.mu.Lock()
	c.creds.Name, c.status.Name = name, name
	if c.creds.Token != "" {
		c.creds.PendingName = creds.PendingName
	}
	c.status.NamePending = c.creds.PendingName != ""
	c.nameRev++
	c.mu.Unlock()
	c.changed()
	return c.Status(), nil
}
func (c *Client) Disconnect() error {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	c.stop()
	c.mu.Lock()
	creds := c.creds
	c.mu.Unlock()
	if creds.Token != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		err := c.request(ctx, creds.URL, "/api/agent/disconnect", creds.Token, map[string]any{}, nil)
		cancel()
		if err != nil && !errors.Is(err, errUnauthorized) {
			_ = c.start()
			return err
		}
	}
	for _, name := range []string{"cloud.json", "cloud-results.json", relayConfigFile} {
		if err := os.Remove(filepath.Join(c.opts.Dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	c.mu.Lock()
	c.creds.Token, c.creds.DeviceID, c.creds.Email, c.creds.PendingName = "", "", "", ""
	c.results = nil
	c.relay = RelayStatus{State: relayStateOff}
	c.status = Status{URL: c.creds.URL, Name: c.creds.Name, State: "signed_out", Message: "已断开云端账号", Relay: c.relay}
	c.mu.Unlock()
	c.changed()
	return nil
}
func (c *Client) run(ctx context.Context, done chan struct{}, creds credentials) {
	defer close(done)
	c.runLoop(ctx, creds)
}
func (c *Client) runLoop(ctx context.Context, creds credentials) {
	// The relay socket lives and dies with the heartbeat loop (sign-out, revocation, exit).
	ctx, stopRelay := context.WithCancel(ctx)
	var relayDone sync.WaitGroup
	relayDone.Add(1)
	go func() { defer relayDone.Done(); c.relayLoop(ctx, creds) }()
	defer func() { stopRelay(); relayDone.Wait() }()
	backoff := c.opts.Interval
	for ctx.Err() == nil {
		err := c.heartbeat(ctx, creds)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errUnauthorized) {
			c.setState("revoked", "设备已被云端解绑，请断开后重新登录")
			return
		}
		if err != nil {
			c.setState("offline", "无法连接云端，将自动重试")
			backoff *= 2
			if backoff > time.Minute {
				backoff = time.Minute
			}
		} else {
			backoff = c.opts.Interval
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (c *Client) heartbeat(ctx context.Context, creds credentials) error {
	c.mu.Lock()
	pending := append([]Result(nil), c.results...)
	pendingName, nameRev := c.creds.PendingName, c.nameRev
	c.mu.Unlock()
	var reply struct {
		Command *Command `json:"command"`
		Name    string   `json:"name"`
	}
	var snapshot any
	if c.opts.Snapshot != nil {
		snapshot = c.opts.Snapshot()
	}
	input := map[string]any{"snapshot": snapshot, "results": pending}
	if pendingName != "" {
		input["name"] = pendingName
	}
	if err := c.request(ctx, creds.URL, "/api/agent/heartbeat", creds.Token, input, &reply); err != nil {
		return err
	}
	// Remove acknowledged results only after the server response. A lost response
	// safely resends receipts; an executing command is never delivered twice.
	c.mu.Lock()
	c.results = nil
	c.status.LastHeartbeat = time.Now()
	// The cloud owns the name, so renames from the web console land here. Skip the
	// reply when a local rename happened meanwhile, and keep a pending rename until
	// the cloud confirms it.
	var renamed *credentials
	if reply.Name != "" && c.nameRev == nameRev && (pendingName == "" || reply.Name == pendingName) && (reply.Name != c.creds.Name || c.creds.PendingName != "") {
		c.creds.Name, c.creds.PendingName = reply.Name, ""
		c.status.Name, c.status.NamePending = reply.Name, false
		saved := c.creds
		renamed = &saved
	}
	c.mu.Unlock()
	if renamed != nil {
		// Persist so a restart shows the current name before the first heartbeat.
		if err := save(c.opts.Dir, "cloud.json", *renamed); err != nil {
			return err
		}
	}
	if err := save(c.opts.Dir, "cloud-results.json", []Result{}); err != nil {
		return err
	}
	c.setState("online", "已连接云端 · 每 15 秒发送心跳")
	if cmd := reply.Command; cmd != nil {
		// Before side effects, persist a crash receipt. On restart report an unknown
		// result instead of repeating an operation whose effects may have occurred.
		result := Result{ID: cmd.ID, Error: "应用在执行期间退出，结果未确认"}
		if err := save(c.opts.Dir, "cloud-results.json", []Result{result}); err != nil {
			return err
		}
		err := errors.New("未知的云端命令")
		if c.opts.Execute != nil {
			err = c.opts.Execute(*cmd)
		}
		result.Error = ""
		if err != nil {
			result.Error = err.Error()
		}
		c.mu.Lock()
		c.results = []Result{result}
		c.mu.Unlock()
		if err := save(c.opts.Dir, "cloud-results.json", []Result{result}); err != nil {
			return err
		}
		c.changed()
	}
	return nil
}

var (
	errUnauthorized = errors.New("设备凭证已失效")
	errOffline      = errors.New("无法连接云端服务")
)

func (c *Client) request(ctx context.Context, base, path, token string, input, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.opts.Client.Do(req)
	if err != nil {
		return errOffline
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return errUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("云端请求失败（%d）", resp.StatusCode)
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(output)
	}
	return nil
}
func save(dir, name string, value any) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".cloud-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, name))
}
