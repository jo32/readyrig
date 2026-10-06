package chromemcp

import (
	"computer-use-server/internal/harness"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
type Bridge struct {
	registry   *harness.Registry
	opts       Options
	mu         sync.Mutex
	status     Status
	client     *client
	key        string
	cancel     context.CancelFunc
	done       chan struct{}
	wake       chan struct{}
	startOnce  sync.Once
	retryAfter time.Time
	roots      func() []string
	// linked run after Refresh, so another browser provider follows the same triggers.
	linked []func()
	// Dependencies remain injectable for deterministic tests without controlling a browser.
	detect  func(context.Context, Options) (target, error)
	launch  func(context.Context, string, []string, []string) (*client, error)
	resolve func(Options, target) (string, []string, []string, error)
}

func New(r *harness.Registry) *Bridge {
	return &Bridge{registry: r, status: Status{State: "waiting", Message: "等待检测 Chrome 远程调试"}, wake: make(chan struct{}, 1), detect: discover, launch: startClient, resolve: command}
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
// approved projects. Call it before Start; RootsChanged reports later changes.
func (b *Bridge) SetRoots(fn func() []string) {
	b.mu.Lock()
	b.roots = fn
	b.mu.Unlock()
}

// RootsChanged makes the connected Chrome DevTools server fetch the folders again.
func (b *Bridge) RootsChanged() {
	b.mu.Lock()
	c := b.client
	b.mu.Unlock()
	if c != nil {
		c.notifyRootsChanged()
	}
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
func (b *Bridge) disconnect() {
	b.mu.Lock()
	c := b.client
	b.client = nil
	b.key = ""
	b.mu.Unlock()
	if c != nil {
		b.registry.ReplaceTools("browser", "chrome_", nil)
		c.Close()
		<-c.exited
	}
}
func (b *Bridge) run(ctx context.Context) {
	defer close(b.done)
	defer b.disconnect()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		b.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-b.wake:
			b.retryAfter = time.Time{}
		}
	}
}
func (b *Bridge) reconcile(ctx context.Context) {
	paused, enabled := b.registry.State()
	if !enabled["browser"] {
		b.disconnect()
		b.setStatus(Status{State: "disabled", Message: "Chrome DevTools MCP 已关闭"})
		return
	}
	if paused {
		return
	}
	if time.Now().Before(b.retryAfter) {
		return
	}
	t, err := b.detect(ctx, b.opts)
	connection := Status{State: "ready", Message: "工具已接入；首次使用时请允许 Chrome 的连接请求。"}
	if err != nil {
		connection = Status{State: "waiting", Message: err.Error()}
		if errors.Is(err, ErrDebugPermission) {
			connection.State = "permission_required"
		}
		// Tool metadata is available before Chrome connects. Use attachment mode
		// while waiting, so listing tools can never launch another browser.
		t = target{Key: "catalog|" + b.opts.UserDataDir + "|" + b.opts.BrowserURL, Args: []string{"--autoConnect", "--channel=stable"}}
		if b.opts.UserDataDir != "" {
			t.Args = append(t.Args, "--user-data-dir="+b.opts.UserDataDir)
		}
	}
	b.mu.Lock()
	c, key, count := b.client, b.key, b.status.Tools
	b.mu.Unlock()
	if c != nil && key == t.Key {
		select {
		case <-c.done:
		default:
			connection.Tools = count
			b.setStatus(connection)
			return
		}
	}
	b.disconnect()
	path, args, env, err := b.resolve(b.opts, t)
	if err != nil {
		b.setStatus(Status{State: "unavailable", Message: err.Error()})
		return
	}
	b.setStatus(Status{State: "connecting", Message: "正在启动官方 Chrome DevTools MCP，首次准备可能需要下载组件…"})
	c, err = b.launch(ctx, path, args, env)
	if err == nil {
		initCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		b.mu.Lock()
		roots := b.roots
		b.mu.Unlock()
		c.setRoots(roots)
		err = c.initialize(initCtx)
		var tools []harness.Tool
		if err == nil {
			tools, err = b.loadTools(initCtx, c)
		}
		cancel()
		if err == nil {
			// Do not publish a connection enabled before a user paused or disabled it.
			paused, enabled = b.registry.State()
			if ctx.Err() != nil || paused || !enabled["browser"] {
				c.Close()
				<-c.exited
				return
			}
			b.mu.Lock()
			b.client = c
			b.key = t.Key
			b.mu.Unlock()
			b.registry.ReplaceTools("browser", "chrome_", tools)
			connection.Tools = len(tools)
			b.setStatus(connection)
			return
		}
		c.Close()
		<-c.exited
	}
	b.setStatus(Status{State: "error", Message: fmt.Sprintf("Chrome MCP 连接失败：%v", err)})
	b.retryAfter = time.Now().Add(30 * time.Second)
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

// pathHint explains the one refusal that is about ReadyRig's setup, not the page.
func pathHint(result map[string]any) string {
	content, _ := result["content"].([]any)
	for _, item := range content {
		if m, _ := item.(map[string]any); m != nil {
			if text, _ := m["text"].(string); strings.Contains(text, "configured workspace roots") {
				return ". File paths must be inside an approved project (see list_projects) or the OS temp folder"
			}
		}
	}
	return ""
}

var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,57}$`)

// advancedTools are the DevTools inspection tools that most browsing tasks never
// need. They stay callable (directly or through use_tool) but are left out of
// tools/list so the everyday browser tools are not buried in schemas.
var advancedTools = map[string]bool{
	"get_console_message": true, "list_console_messages": true,
	"get_network_request": true, "list_network_requests": true,
	"get_css_styles": true, "emulate": true, "resize_page": true,
	"lighthouse_audit": true, "take_heapsnapshot": true,
	"performance_analyze_insight": true, "performance_start_trace": true, "performance_stop_trace": true,
}

func groupOf(name string) string {
	if advancedTools[name] {
		return "advanced"
	}
	return ""
}

func (b *Bridge) loadTools(ctx context.Context, c *client) ([]harness.Tool, error) {
	var out []harness.Tool
	seen := map[string]bool{}
	cursor := ""
	cursors := map[string]bool{}
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools []struct {
				Name         string         `json:"name"`
				Description  string         `json:"description"`
				InputSchema  map[string]any `json:"inputSchema"`
				OutputSchema map[string]any `json:"outputSchema"`
				Annotations  map[string]any `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &page) != nil {
			return nil, errors.New("Chrome MCP 工具清单无效")
		}
		for _, t := range page.Tools {
			if !toolName.MatchString(t.Name) || seen[t.Name] || t.InputSchema["type"] != "object" {
				return nil, errors.New("Chrome MCP 工具定义无效或重复")
			}
			seen[t.Name] = true
			name := t.Name
			readOnly, _ := t.Annotations["readOnlyHint"].(bool)
			out = append(out, harness.Tool{Spec: harness.Spec{Name: "chrome_" + name, Description: t.Description, Category: "browser", InputSchema: t.InputSchema, OutputSchema: t.OutputSchema, Annotations: t.Annotations, Mutating: !readOnly, Parallel: false, Group: groupOf(name)}, External: true, Run: func(ctx context.Context, in harness.Invocation) (harness.Output, error) {
				b.mu.Lock()
				available := b.client == c && b.status.State == "ready"
				message := b.status.Message
				b.mu.Unlock()
				if !available {
					return harness.Output{}, &harness.ToolError{Code: "browser_not_ready", Message: "Chrome is not ready: " + message}
				}
				// A stale tool reference must never reconnect and silently repeat an action.
				callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				raw, err := c.call(callCtx, "tools/call", map[string]any{"name": name, "arguments": in.Arguments})
				if err != nil {
					b.Refresh()
					return harness.Output{}, err
				}
				var result map[string]any
				if json.Unmarshal(raw, &result) != nil || result == nil {
					return harness.Output{}, errors.New("Chrome DevTools MCP returned an invalid result")
				}
				out := harness.Output{Value: result, MCPResult: result}
				if failed, _ := result["isError"].(bool); failed {
					return out, &harness.ToolError{Code: "browser_tool_failed", Message: "Chrome DevTools: " + firstLine(resultText(result), "the tool reported an error") + pathHint(result)}
				}
				return out, nil
			}})
		}
		if len(out) > 256 {
			return nil, errors.New("Chrome MCP 工具数超出上限")
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
		if cursors[cursor] {
			return nil, errors.New("Chrome MCP 工具分页重复")
		}
		cursors[cursor] = true
		if len(cursors) > 32 {
			return nil, errors.New("Chrome MCP 工具分页超出上限")
		}
	}
	if len(out) == 0 {
		return nil, errors.New("Chrome MCP 未提供工具")
	}
	return out, nil
}
