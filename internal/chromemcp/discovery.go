// Package chromemcp bridges the official Chrome DevTools MCP over stdio.
package chromemcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const Package = "chrome-devtools-mcp@1.10.1"

type Options struct {
	Disabled    bool
	BrowserURL  string
	UserDataDir string
	// Command is an optional installed chrome-devtools-mcp executable (never a shell string).
	Command string
}
type target struct {
	Key  string
	Args []string
}

func localURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("Chrome 调试地址必须是本机 HTTP 地址，例如 http://127.0.0.1:9222")
	}
	host := u.Hostname()
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("Chrome 调试地址只允许 localhost 或回环 IP")
		}
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("Chrome 调试地址需要有效端口")
	}
	return strings.TrimRight(raw, "/"), nil
}

func chromePaths() (binaries, profiles []string) {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		binaries = []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome")}
		profiles = []string{filepath.Join(home, "Library/Application Support/Google/Chrome")}
	case "windows":
		for _, base := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
			if base != "" {
				binaries = append(binaries, filepath.Join(base, "Google/Chrome/Application/chrome.exe"))
			}
		}
		profiles = []string{filepath.Join(os.Getenv("LOCALAPPDATA"), "Google/Chrome/User Data")}
	default:
		for _, name := range []string{"google-chrome", "google-chrome-stable"} {
			if p, err := exec.LookPath(name); err == nil {
				binaries = append(binaries, p)
			}
		}
		config := os.Getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		profiles = []string{filepath.Join(config, "google-chrome")}
	}
	return
}
func discover(ctx context.Context, opts Options) (target, error) {
	bins, profiles := chromePaths()
	return discoverAt(ctx, opts, bins, profiles)
}

var ErrDebugPermission = errors.New("Chrome 调试入口需要授权：macOS 阻止了读取 DevToolsActivePort。请在 ReadyRig 中点击「允许访问」选择该文件；若仍被拒绝，请检查系统设置「隐私与安全性」中的 ReadyRig 数据访问权限，然后重新检测。网页模式请为启动 ReadyRig 的终端授予相应权限。")

func readDebugMarker(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 4097))
}

func discoverAt(ctx context.Context, opts Options, bins, profiles []string) (target, error) {
	return discoverWithReader(ctx, opts, bins, profiles, readDebugMarker)
}

func discoverWithReader(ctx context.Context, opts Options, bins, profiles []string, readMarker func(string) ([]byte, error)) (target, error) {
	installed := false
	for _, p := range bins {
		if s, e := os.Stat(p); e == nil && !s.IsDir() {
			installed = true
			break
		}
	}
	if !installed {
		return target{}, fmt.Errorf("未检测到本地 Google Chrome")
	}
	var explicitErr error
	var explicitPort string
	if opts.BrowserURL != "" {
		base, err := localURL(opts.BrowserURL)
		if err != nil {
			return target{}, err
		}
		if t, err := probeURL(ctx, base); err == nil {
			return t, nil
		} else {
			explicitErr = err
		}
		u, _ := url.Parse(base)
		explicitPort = u.Port()
	}
	// Prefer the default local server without touching Chrome's protected profile.
	// Explicit profiles must not silently attach to a different browser.
	if opts.UserDataDir == "" && opts.BrowserURL == "" {
		if t, err := probeURL(ctx, "http://127.0.0.1:9222"); err == nil {
			return t, nil
		}
	}
	if opts.UserDataDir != "" {
		profiles = []string{opts.UserDataDir}
	}
	var readErr error
	for _, dir := range profiles {
		// This file contains only a port and WebSocket path; never read browsing data.
		b, err := readMarker(filepath.Join(dir, "DevToolsActivePort"))
		if err != nil {
			if !os.IsNotExist(err) {
				readErr = err
			}
			continue
		}
		if len(b) > 4096 {
			continue
		}
		lines := strings.Fields(string(b))
		if len(lines) != 2 {
			continue
		}
		port, err := strconv.Atoi(lines[0])
		if err != nil || port < 1 || port > 65535 || !strings.HasPrefix(lines[1], "/devtools/browser/") {
			continue
		}
		if explicitPort != "" && explicitPort != strconv.Itoa(port) {
			continue
		}
		wsPath, err := url.Parse(lines[1])
		if err != nil || wsPath.RawQuery != "" || wsPath.Fragment != "" || wsPath.Host != "" || wsPath.Scheme != "" || strings.TrimPrefix(wsPath.Path, "/devtools/browser/") == "" {
			continue
		}
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		conn, err := (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext(ctx, "tcp", addr)
		if err != nil {
			continue
		}
		conn.Close()
		// Pass the validated endpoint so Node does not need a second grant to
		// read Chrome's protected data directory. Chrome still asks to allow CDP.
		endpoint := "ws://" + addr + lines[1]
		return target{Key: endpoint, Args: []string{"--ws-endpoint=" + endpoint}}, nil
	}
	if errors.Is(readErr, os.ErrPermission) {
		return target{}, ErrDebugPermission
	}
	if readErr != nil {
		return target{}, fmt.Errorf("无法读取 Chrome 调试入口：%w", readErr)
	}
	if explicitErr != nil {
		return target{}, explicitErr
	}
	return target{}, fmt.Errorf("等待 Chrome 开启远程调试：chrome://inspect/#remote-debugging")
}
func probeURL(ctx context.Context, raw string) (target, error) {
	base, err := localURL(raw)
	if err != nil {
		return target{}, err
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/json/version", nil)
	if err != nil {
		return target{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return target{}, fmt.Errorf("Chrome 调试入口不可用：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		res.Body.Close()
		return probeApprovalServer(ctx, client, base)
	}
	var version struct {
		Browser              string
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&version) != nil || !strings.Contains(version.Browser, "Chrome/") {
		return target{}, fmt.Errorf("此端口没有提供 Chrome 调试服务")
	}
	ws, err := url.Parse(version.WebSocketDebuggerURL)
	if err != nil || ws.Scheme != "ws" || ws.User != nil || ws.RawQuery != "" || ws.Fragment != "" || !strings.HasPrefix(ws.Path, "/devtools/browser/") {
		return target{}, fmt.Errorf("无效的 Chrome WebSocket 调试地址")
	}
	if _, err = localURL("http://" + ws.Host); err != nil {
		return target{}, err
	}
	// Use the validated endpoint, avoiding another discovery/redirect in the child.
	return target{Key: version.WebSocketDebuggerURL, Args: []string{"--ws-endpoint=" + version.WebSocketDebuggerURL}}, nil
}

func probeApprovalServer(ctx context.Context, client *http.Client, base string) (target, error) {
	// Approval mode disables /json/version and accepts /devtools/browser without
	// a GUID. Probe an unsupported path: Chrome rejects it before asking for
	// approval, so periodic discovery never opens a connection permission dialog.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/devtools/readyrig-probe", nil)
	if err != nil {
		return target{}, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return target{}, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString(nonce[:]))
	res, err := client.Do(req)
	if err != nil {
		return target{}, fmt.Errorf("Chrome 调试入口不可用：%w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 256))
	if err != nil || res.StatusCode != http.StatusForbidden || string(body) != "Connection rejected" {
		return target{}, fmt.Errorf("此端口没有提供 Chrome 调试服务")
	}
	endpoint := "ws" + strings.TrimPrefix(base, "http") + "/devtools/browser"
	return target{Key: endpoint, Args: []string{"--ws-endpoint=" + endpoint}}, nil
}
