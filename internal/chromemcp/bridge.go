package chromemcp

import (
	"computer-use-server/internal/harness"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Status struct {
	State   string `json:"state"`
	Message string `json:"message"`
	Tools   int    `json:"tools"`
}

// Bridge publishes the built-in Chrome tools and drives the user's running
// Chrome through its remote debugging port. It needs no helper program.
//
// The tools are listed as soon as the browser capability is on. Chrome is not
// contacted beyond a harmless probe until a tool is called: the first call
// opens the debugging connection, which Chrome asks the user to allow, and the
// connection is then kept so that Chrome asks only once.
type Bridge struct {
	registry  *harness.Registry
	opts      Options
	mu        sync.Mutex
	status    Status
	endpoint  string
	browser   *chrome
	published int
	cancel    context.CancelFunc
	done      chan struct{}
	wake      chan struct{}
	startOnce sync.Once
	roots     func() []string
	connMu    sync.Mutex // serializes opening the connection
	// linked run after Refresh, so another browser provider follows the same triggers.
	linked []func()
	// Injectable so tests need no Chrome.
	detect func(context.Context, Options) (target, error)
	dial   func(context.Context, string, func(session, method string, params json.RawMessage)) (*cdpConn, error)
}

func New(r *harness.Registry) *Bridge {
	return &Bridge{registry: r, status: Status{State: "waiting", Message: "等待检测 Chrome 远程调试"}, wake: make(chan struct{}, 1), detect: discover, dial: dialCDP}
}
func (b *Bridge) Start(opts Options) error {
	if opts.BrowserURL != "" {
		if _, err := localURL(opts.BrowserURL); err != nil {
			return err
		}
	}
	b.startOnce.Do(func() {
		// A startup opt-out can still be changed explicitly in the local console.
		if opts.Disabled {
			_ = b.registry.Enable("browser", false)
		}
		ctx, cancel := context.WithCancel(context.Background())
		b.mu.Lock()
		b.opts = opts
		b.cancel = cancel
		b.done = make(chan struct{})
		b.mu.Unlock()
		go b.run(ctx)
	})
	return nil
}

// SetRoots sets the folders the browser tools may read and write, normally the
// approved projects. It is asked on every call, so changes apply at once.
func (b *Bridge) SetRoots(fn func() []string) {
	b.mu.Lock()
	b.roots = fn
	b.mu.Unlock()
}

func (b *Bridge) currentRoots() []string {
	b.mu.Lock()
	fn := b.roots
	b.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

func (b *Bridge) Status() Status { b.mu.Lock(); defer b.mu.Unlock(); return b.status }

// Link makes fn run whenever Refresh is called.
func (b *Bridge) Link(fn func()) {
	b.mu.Lock()
	b.linked = append(b.linked, fn)
	b.mu.Unlock()
}
func (b *Bridge) Refresh() {
	b.mu.Lock()
	linked := append([]func(){}, b.linked...)
	b.mu.Unlock()
	for _, fn := range linked {
		fn()
	}
	select {
	case b.wake <- struct{}{}:
	default:
	}
}
func (b *Bridge) Close() {
	b.mu.Lock()
	cancel, done := b.cancel, b.done
	b.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
func (b *Bridge) setStatus(s Status) {
	b.mu.Lock()
	changed := b.status != s
	b.status = s
	b.mu.Unlock()
	if changed {
		b.registry.Signal()
	}
}

// disconnect closes the debugging connection, or only the given one. Chrome
// and its pages stay as they are.
func (b *Bridge) disconnect(only *chrome) {
	b.mu.Lock()
	br := b.browser
	if br == nil || only != nil && br != only {
		b.mu.Unlock()
		return
	}
	b.browser = nil
	b.mu.Unlock()
	br.conn.Close()
}

func (b *Bridge) run(ctx context.Context) {
	defer close(b.done)
	defer b.unpublish()
	defer b.disconnect(nil)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		b.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-b.wake:
		}
	}
}

func (b *Bridge) publish() int {
	b.mu.Lock()
	count := b.published
	b.mu.Unlock()
	if count > 0 {
		return count
	}
	catalog := chromeCatalog()
	tools := make([]harness.Tool, 0, len(catalog))
	for _, t := range catalog {
		group := ""
		if t.advanced {
			group = "advanced"
		}
		tools = append(tools, harness.Tool{Spec: harness.Spec{Name: "chrome_" + t.name, Description: t.description, Category: "browser", InputSchema: t.schema, Annotations: map[string]any{"readOnlyHint": t.readOnly}, Mutating: !t.readOnly, Parallel: false, Group: group}, External: true, Run: b.handler(t)})
	}
	b.registry.ReplaceTools("browser", "chrome_", tools)
	b.mu.Lock()
	b.published = len(tools)
	b.mu.Unlock()
	return len(tools)
}

func (b *Bridge) unpublish() {
	b.mu.Lock()
	was := b.published
	b.published = 0
	b.mu.Unlock()
	if was > 0 { // announce the change once, not on every tick
		b.registry.ReplaceTools("browser", "chrome_", nil)
	}
}

func (b *Bridge) reconcile(ctx context.Context) {
	paused, enabled := b.registry.State()
	if !enabled["browser"] {
		b.disconnect(nil)
		b.unpublish()
		b.setStatus(Status{State: "disabled", Message: "Chrome 浏览器工具已关闭"})
		return
	}
	if paused {
		// A paused computer holds no control over the browser.
		b.disconnect(nil)
		return
	}
	count := b.publish()
	t, err := b.detect(ctx, b.opts)
	b.mu.Lock()
	br := b.browser
	connected := br != nil && br.conn.alive()
	if err == nil {
		b.endpoint = t.Endpoint
	}
	moved := err == nil && connected && br.endpoint != t.Endpoint
	b.mu.Unlock()
	switch {
	case moved:
		// Chrome restarted on another address: the old connection is of no use.
		b.disconnect(br)
	case err != nil && connected:
		// One missed probe must not drop a working connection, which would make
		// Chrome ask the user for permission again.
		err = nil
	}
	status := Status{State: "ready", Message: "工具已接入；首次使用时请允许 Chrome 的连接请求。", Tools: count}
	if err != nil {
		status = Status{State: "waiting", Message: err.Error(), Tools: count}
		if errors.Is(err, ErrDebugPermission) {
			status.State = "permission_required"
		}
	}
	b.setStatus(status)
}

// connect returns the live debugging connection, opening it on the first call.
func (b *Bridge) connect(ctx context.Context) (*chrome, error) {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	b.mu.Lock()
	br, endpoint := b.browser, b.endpoint
	b.mu.Unlock()
	if br != nil && br.conn.alive() {
		return br, nil
	}
	b.disconnect(br)
	if endpoint == "" {
		return nil, errors.New("Chrome's remote debugging was not found")
	}
	// Chrome answers only after the user allows the connection in its dialog.
	dialCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	br = newChrome(nil, b.currentRoots)
	br.endpoint = endpoint
	conn, err := b.dial(dialCtx, endpoint, br.event)
	if err != nil {
		return nil, errors.New("Chrome did not accept the connection. Chrome shows a dialog asking to allow remote debugging: allow it, then call the tool again")
	}
	br.conn = conn
	if _, err = conn.call(dialCtx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}); err != nil {
		conn.Close()
		return nil, err
	}
	// Do not keep a connection opened while the user paused or disabled the browser.
	if paused, enabled := b.registry.State(); paused || !enabled["browser"] {
		conn.Close()
		return nil, errors.New("browser control was turned off")
	}
	b.mu.Lock()
	b.browser = br
	b.mu.Unlock()
	return br, nil
}

func (b *Bridge) handler(t chromeTool) harness.Handler {
	return func(ctx context.Context, in harness.Invocation) (harness.Output, error) {
		b.mu.Lock()
		state, message := b.status.State, b.status.Message
		b.mu.Unlock()
		if state != "ready" {
			return harness.Output{}, &harness.ToolError{Code: "browser_not_ready", Message: "Chrome is not ready: " + message}
		}
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		br, err := b.connect(callCtx)
		if err != nil {
			if ctx.Err() != nil {
				return harness.Output{}, ctx.Err()
			}
			return harness.Output{}, &harness.ToolError{Code: "browser_not_ready", Message: "Chrome is not ready: " + err.Error()}
		}
		result, err := t.run(callCtx, br, in.Arguments)
		if err == nil {
			return harness.Output{Value: result, MCPResult: result}, nil
		}
		var toolErr *harness.ToolError
		switch {
		case ctx.Err() != nil:
			// A cancelled call leaves the connection open; nothing is repeated.
			return harness.Output{}, ctx.Err()
		case !br.conn.alive():
			b.disconnect(br)
			b.Refresh()
			return harness.Output{}, &harness.ToolError{Code: "browser_not_ready", Message: "Chrome closed the debugging connection, so the result of this call is unknown. Check the page before repeating it; the next call reconnects"}
		case errors.Is(err, context.DeadlineExceeded):
			return harness.Output{}, &harness.ToolError{Code: "browser_tool_failed", Message: "Chrome did not answer within two minutes. A dialog on the page or a busy page can cause this; chrome_list_pages still works"}
		case errors.As(err, &toolErr):
			return harness.Output{}, err
		}
		return harness.Output{}, &harness.ToolError{Code: "browser_tool_failed", Message: "Chrome: " + firstLine(err.Error(), "the tool failed")}
	}
}

// firstLine keeps an upstream error readable in the activity log: the first
// non-empty line, without a leading "Error: ", at most 300 bytes.
func firstLine(text, fallback string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Error:")); line != "" {
			if len(line) > 300 {
				line = strings.ToValidUTF8(line[:300], "") + "…"
			}
			return line
		}
	}
	return fallback
}

var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,57}$`)
