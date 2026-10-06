package chromemcp

import (
	"computer-use-server/internal/harness"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Safari 27 and later ships an MCP server inside safaridriver (safaridriver --mcp,
// stdio). SafariBridge publishes its tools as safari_* tools in the browser
// capability, next to the Chrome tools.
//
// Tools are exposed lazily at two levels:
//   - The catalog is read once with a short-lived process. No connection to Safari
//     is opened until a Safari tool is first called, and the connection ends with
//     the app, the capability or a cancelled call.
//   - Only the everyday tools appear in tools/list. The rest are in the advanced
//     group: callable through use_tool and listed by help, but not advertised.
const (
	safariCommand = "/usr/bin/safaridriver"
	safariPrefix  = "safari_"
	// The setting that makes the server drive Safari, named as Safari's settings show it.
	safariSetting = `Safari > Settings > Developer > "Allow remote automation and external agents"`
)

// safariAdvanced are the upstream tool names kept out of tools/list.
var safariAdvanced = map[string]bool{
	"browser_console_messages": true, "browser_dialogs": true, "close_tab": true,
	"evaluate_javascript": true, "get_network_request": true, "list_network_requests": true,
	"list_tabs": true, "page_info": true, "set_emulated_media": true, "set_viewport_size": true,
	"switch_tab": true, "wait_for_navigation": true,
}

// Safari sends no annotations, so say which tools only read.
var safariReadOnly = map[string]bool{
	"browser_console_messages": true, "get_network_request": true, "get_page_content": true,
	"list_network_requests": true, "list_tabs": true, "page_info": true, "screenshot": true,
	"wait_for_navigation": true,
}

type SafariOptions struct {
	Disabled bool
	// Command overrides /usr/bin/safaridriver.
	Command string
}

type SafariBridge struct {
	registry *harness.Registry
	mu       sync.Mutex // guards the fields below
	status   Status
	client   *client
	pub      bool
	roots    func() []string
	opts     SafariOptions
	base     context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	wake     chan struct{}
	start    sync.Once
	retry    time.Time
	connMu   sync.Mutex // serializes opening the live connection
	interval time.Duration
	// Injectable so tests need neither Safari nor macOS 27.
	launch func(context.Context, string, []string, []string) (*client, error)
	probe  func(context.Context, string) error
}

func NewSafari(r *harness.Registry) *SafariBridge {
	return &SafariBridge{registry: r, status: Status{State: "waiting", Message: "Safari MCP 尚未启动"}, wake: make(chan struct{}, 1), interval: 15 * time.Second, launch: startClient, probe: probeSafari}
}

// SetRoots sets the folders a Safari tool may save files into. Without it no
// file is saved, so the Safari server can never write outside approved projects.
func (b *SafariBridge) SetRoots(fn func() []string) {
	b.mu.Lock()
	b.roots = fn
	b.mu.Unlock()
}

func (b *SafariBridge) Start(opts SafariOptions) {
	b.start.Do(func() {
		b.mu.Lock()
		b.opts = opts
		if opts.Command == "" {
			b.opts.Command = safariCommand
		}
		if opts.Disabled {
			b.status = Status{State: "disabled", Message: "Safari MCP 已关闭"}
			b.mu.Unlock()
			return
		}
		if runtime.GOOS != "darwin" && opts.Command == "" {
			b.status = Status{State: "unavailable", Message: "Safari MCP 需要 macOS 27 或更新版本"}
			b.mu.Unlock()
			return
		}
		b.base, b.cancel = context.WithCancel(context.Background())
		b.done = make(chan struct{})
		ctx := b.base
		b.mu.Unlock()
		go b.run(ctx)
	})
}

func (b *SafariBridge) Status() Status { b.mu.Lock(); defer b.mu.Unlock(); return b.status }

// Refresh makes the bridge look at the capability and retry a failed catalog at once.
func (b *SafariBridge) Refresh() {
	b.mu.Lock()
	b.retry = time.Time{}
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *SafariBridge) Close() {
	b.mu.Lock()
	cancel, done := b.cancel, b.done
	b.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (b *SafariBridge) setStatus(s Status) {
	b.mu.Lock()
	changed := b.status != s
	b.status = s
	b.mu.Unlock()
	if changed {
		b.registry.Signal()
	}
}

func (b *SafariBridge) run(ctx context.Context) {
	defer close(b.done)
	defer b.shutdown()
	ticker := time.NewTicker(b.interval)
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

func (b *SafariBridge) shutdown() {
	b.dropClient(nil)
	b.registry.ReplaceTools("safari", safariPrefix, nil)
}

func (b *SafariBridge) reconcile(ctx context.Context) {
	paused, enabled := b.registry.State()
	if !enabled["safari"] {
		b.dropClient(nil)
		b.mu.Lock()
		was := b.pub
		b.pub = false
		b.mu.Unlock()
		if was { // announce the change once, not on every tick
			b.registry.ReplaceTools("safari", safariPrefix, nil)
		}
		b.setStatus(Status{State: "disabled", Message: "Safari MCP 已关闭"})
		return
	}
	b.mu.Lock()
	done, wait := b.pub, time.Now().Before(b.retry)
	b.mu.Unlock()
	if paused || done || wait {
		return
	}
	b.loadCatalog(ctx)
}

// safariTool is one tool of Safari's own catalog.
type safariTool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
	Annotations  map[string]any `json:"annotations"`
}

// loadCatalog reads the tool list with a short-lived process, publishes it and
// lets the process go, so Safari is not touched until a tool is called.
func (b *SafariBridge) loadCatalog(ctx context.Context) {
	b.mu.Lock()
	command := b.opts.Command
	b.mu.Unlock()
	fail := func(state, message string, wait time.Duration) {
		b.mu.Lock()
		b.retry = time.Now().Add(wait)
		b.mu.Unlock()
		b.setStatus(Status{State: state, Message: message})
	}
	if err := b.probe(ctx, command); err != nil {
		fail("unavailable", err.Error(), 10*time.Minute)
		return
	}
	b.setStatus(Status{State: "connecting", Message: "正在读取 Safari 的工具清单"})
	c, err := b.launch(ctx, command, []string{"--mcp"}, os.Environ())
	if err != nil {
		fail("error", fmt.Sprintf("无法启动 Safari MCP：%v", err), 30*time.Second)
		return
	}
	defer func() { c.Close(); <-c.exited }()
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var tools []safariTool
	if err = c.initialize(initCtx); err == nil {
		tools, err = fetchSafariCatalog(initCtx, c)
	}
	if err != nil {
		fail("error", fmt.Sprintf("无法读取 Safari 工具：%v", err), 30*time.Second)
		return
	}
	published, err := b.publish(tools)
	if err != nil {
		fail("error", err.Error(), 5*time.Minute)
		return
	}
	b.mu.Lock()
	b.pub = true
	b.mu.Unlock()
	b.setStatus(Status{State: "ready", Message: "Safari 工具已就绪。首次使用前，请在 Safari「设置 > 开发者」中开启“Allow remote automation and external agents”（允许远程自动化和外部代理）。", Tools: published})
}

func fetchSafariCatalog(ctx context.Context, c *client) ([]safariTool, error) {
	var all []safariTool
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 32; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var list struct {
			Tools      []safariTool `json:"tools"`
			NextCursor string       `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &list) != nil {
			return nil, errors.New("Safari 工具清单无效")
		}
		all = append(all, list.Tools...)
		if len(all) > 256 {
			return nil, errors.New("Safari 提供的工具过多")
		}
		cursor = list.NextCursor
		if cursor == "" || seen[cursor] {
			break
		}
		seen[cursor] = true
	}
	if len(all) == 0 {
		return nil, errors.New("Safari 没有提供工具")
	}
	return all, nil
}

// safariName is the ReadyRig name of an upstream tool: safari_ plus the name
// without its browser_ prefix, which would repeat itself.
func safariName(upstream string) string {
	return safariPrefix + strings.TrimPrefix(upstream, "browser_")
}

func (b *SafariBridge) publish(tools []safariTool) (int, error) {
	out := make([]harness.Tool, 0, len(tools))
	names := map[string]bool{}
	for _, t := range tools {
		name := safariName(t.Name)
		if !toolName.MatchString(name) || names[name] || t.InputSchema["type"] != "object" {
			return 0, errors.New("Safari 工具定义无效或重复")
		}
		names[name] = true
		upstream, readOnly := t.Name, safariReadOnly[t.Name]
		annotations := t.Annotations
		if readOnly {
			annotations = map[string]any{"readOnlyHint": true}
		}
		description := t.Description
		if upstream == "create_tab" {
			description += " Safari controls its own window: start with this or safari_navigate_to_url, because other tools can fail before a window is open."
		}
		group := ""
		if safariAdvanced[upstream] {
			group = "advanced"
		}
		out = append(out, harness.Tool{Spec: harness.Spec{Name: name, Description: description, Category: "safari", InputSchema: t.InputSchema, OutputSchema: t.OutputSchema, Annotations: annotations, Mutating: !readOnly, Parallel: false, Group: group}, External: true, Run: func(ctx context.Context, in harness.Invocation) (harness.Output, error) {
			return b.callTool(ctx, upstream, in)
		}})
	}
	b.registry.ReplaceTools("safari", safariPrefix, out)
	return len(out), nil
}

// dropClient closes the live connection (only c when given) so the next call opens a new one.
func (b *SafariBridge) dropClient(only *client) {
	b.mu.Lock()
	c := b.client
	if c == nil || only != nil && c != only {
		b.mu.Unlock()
		return
	}
	b.client = nil
	b.mu.Unlock()
	c.Close()
	<-c.exited
}

// ensureClient opens the live connection on the first call and reuses it after that.
func (b *SafariBridge) ensureClient() (*client, error) {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	b.mu.Lock()
	c, base, command := b.client, b.base, b.opts.Command
	b.mu.Unlock()
	if base == nil {
		return nil, errors.New("Safari MCP is not running")
	}
	if c != nil {
		select {
		case <-c.done:
			b.dropClient(c)
		default:
			return c, nil
		}
	}
	c, err := b.launch(base, command, []string{"--mcp"}, os.Environ())
	if err != nil {
		return nil, err
	}
	initCtx, cancel := context.WithTimeout(base, 30*time.Second)
	defer cancel()
	if err = c.initialize(initCtx); err != nil {
		c.Close()
		<-c.exited
		return nil, err
	}
	b.mu.Lock()
	b.client = c
	b.mu.Unlock()
	return c, nil
}

func (b *SafariBridge) callTool(ctx context.Context, upstream string, in harness.Invocation) (harness.Output, error) {
	if err := b.checkSavePath(in.Arguments); err != nil {
		return harness.Output{}, err
	}
	c, err := b.ensureClient()
	if err != nil {
		return harness.Output{}, &harness.ToolError{Code: "browser_not_ready", Message: "Safari is not ready: " + err.Error()}
	}
	// A failed call never reconnects and repeats an action by itself.
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	arguments := in.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	raw, err := c.call(callCtx, "tools/call", map[string]any{"name": upstream, "arguments": arguments})
	if err != nil {
		b.dropClient(c)
		return harness.Output{}, err
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return harness.Output{}, errors.New("Safari returned an invalid result")
	}
	out := harness.Output{Value: result, MCPResult: result}
	failed, _ := result["isError"].(bool)
	if !failed {
		b.markWorking()
		return out, nil
	}
	message := "Safari: " + firstLine(resultText(result), "the tool reported an error")
	if strings.Contains(resultText(result), "Allow remote automation") {
		message += ". Turn on " + safariSetting + ", then try again"
		b.mu.Lock()
		tools := b.status.Tools
		b.mu.Unlock()
		b.setStatus(Status{State: "permission_required", Message: "请在 Safari「设置 > 开发者」中开启“Allow remote automation and external agents”（允许远程自动化和外部代理），Safari 才会接受连接。", Tools: tools})
	}
	return out, &harness.ToolError{Code: "browser_tool_failed", Message: message}
}

// markWorking clears a permission warning once Safari has answered a call.
func (b *SafariBridge) markWorking() {
	b.mu.Lock()
	tools, state := b.status.Tools, b.status.State
	b.mu.Unlock()
	if state == "permission_required" {
		b.setStatus(Status{State: "ready", Message: "Safari 已连接。", Tools: tools})
	}
}

func resultText(result map[string]any) string {
	var sb strings.Builder
	content, _ := result["content"].([]any)
	for _, item := range content {
		if m, _ := item.(map[string]any); m != nil {
			if text, _ := m["text"].(string); text != "" {
				sb.WriteString(text)
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}

// checkSavePath keeps the files Safari writes (screenshots, page content) inside
// approved projects. Its server has no notion of roots, so ReadyRig enforces it.
func (b *SafariBridge) checkSavePath(arguments json.RawMessage) error {
	var args map[string]any
	if len(arguments) == 0 || json.Unmarshal(arguments, &args) != nil {
		return nil
	}
	raw, present := args["savePath"]
	if !present || raw == nil {
		return nil
	}
	path, _ := raw.(string)
	b.mu.Lock()
	roots := b.roots
	b.mu.Unlock()
	var allowed []string
	if roots != nil {
		allowed = roots()
	}
	if path == "" || !filepath.IsAbs(path) || !withinRoots(path, allowed) {
		return &harness.ToolError{Code: "path_outside_project", Message: "savePath must be an absolute path inside an approved project (see list_projects)"}
	}
	return nil
}

// withinRoots reports whether path lies strictly inside one of the roots once
// symbolic links are resolved. The file itself may not exist yet.
func withinRoots(path string, roots []string) bool {
	target, err := resolveExisting(path)
	if err != nil {
		return false
	}
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		real, err := filepath.EvalSymlinks(filepath.Clean(root))
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(real, target)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolveExisting resolves symbolic links in the longest part of path that exists.
func resolveExisting(path string) (string, error) {
	path = filepath.Clean(path)
	rest := ""
	for {
		real, err := filepath.EvalSymlinks(path)
		if err == nil {
			return filepath.Join(real, rest), nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		rest = filepath.Join(filepath.Base(path), rest)
		path = parent
	}
}

// probeSafari checks that this Mac has a Safari with an MCP server.
func probeSafari(ctx context.Context, command string) error {
	if _, err := os.Stat(command); err != nil {
		return errors.New("未找到 safaridriver；Safari MCP 需要 macOS 27 或更新版本")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, "--help").CombinedOutput()
	if err != nil && len(out) == 0 {
		return fmt.Errorf("无法运行 safaridriver：%w", err)
	}
	if !strings.Contains(string(out), "--mcp") {
		return errors.New("当前 Safari 没有内置 MCP 服务；需要 Safari 27 或更新版本")
	}
	return nil
}
