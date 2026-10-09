//go:build !nogui

package desktop

import (
	"computer-use-server/internal/brand"
	"computer-use-server/internal/harness"
	"computer-use-server/internal/i18n"
	"computer-use-server/internal/update"
	"encoding/json"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const Available = true

func Run(handler http.Handler, registry *harness.Registry, updates *update.Manager, shutdown func(), languagePath string) error {
	languages := i18n.New(languagePath)
	tr := languages.Text
	var applyLanguage func()
	done := make(chan struct{})
	stop := sync.OnceFunc(func() { close(done) })
	defer stop()
	var showMain func()
	var app *application.App
	var window *application.WebviewWindow
	var authorizing atomic.Bool
	var choosingDirectory atomic.Bool
	exports := &exportManager{handler: handler, choose: func() (string, error) {
		application.InvokeSync(showMain)
		return app.Dialog.SaveFile().AttachToWindow(window).SetMessage(tr("导出")).SetFilename("readyrig-calls.ndjson").CanCreateDirectories(true).PromptForSingleSelection()
	}}
	defer exports.close()
	// This endpoint only exists in the native webview, never in the public gateway.
	native := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/window/export" {
			exports.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/window/select-directory" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if !choosingDirectory.CompareAndSwap(false, true) {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": tr("目录选择窗口已打开")})
				return
			}
			defer choosingDirectory.Store(false)
			home, _ := os.UserHomeDir()
			application.InvokeSync(showMain)
			chosen, err := app.Dialog.OpenFile().AttachToWindow(window).SetTitle(tr("添加文件夹")).SetButtonText(tr("添加文件夹")).SetDirectory(home).CanChooseFiles(false).CanChooseDirectories(true).PromptForSingleSelection()
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": tr("无法打开系统文件选择器")})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"path": chosen})
			return
		}
		if r.URL.Path == "/api/window/open-url" && r.Method == http.MethodPost {
			var in struct {
				URL string `json:"url"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
				http.Error(w, "invalid URL", 400)
				return
			}
			u, err := url.Parse(in.URL)
			if err != nil || u.User != nil || u.Host == "" || u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
				http.Error(w, "invalid URL", 400)
				return
			}
			if err := app.Browser.OpenURL(in.URL); err != nil {
				http.Error(w, "unable to open browser", 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if r.URL.Path == "/api/window/language" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			switch r.Method {
			case http.MethodGet:
				_ = json.NewEncoder(w).Encode(languages.Selection())
			case http.MethodPost:
				var selection i18n.Selection
				decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&selection); err != nil || !i18n.ValidPreference(selection.Preference) || !i18n.ValidLocale(selection.Locale) {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid language selection"})
					return
				}
				if err := languages.Set(selection); err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": tr("无法保存语言偏好")})
					return
				}
				if applyLanguage != nil {
					application.InvokeAsync(applyLanguage)
				}
				_ = json.NewEncoder(w).Encode(languages.Selection())
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
			return
		}
		if r.URL.Path == "/api/window/open" && r.Method == http.MethodPost {
			application.InvokeAsync(showMain)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if r.URL.Path == "/api/chrome/authorize" && r.Method == http.MethodPost && runtime.GOOS == "darwin" {
			fail := func(message string, status int) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
			}
			if !authorizing.CompareAndSwap(false, true) {
				fail(tr("授权窗口已打开"), http.StatusConflict)
				return
			}
			defer authorizing.Store(false)
			home, err := os.UserHomeDir()
			if err != nil {
				fail(tr("无法定位 Chrome 目录"), 500)
				return
			}
			dir := filepath.Join(home, "Library/Application Support/Google/Chrome")
			chosen, err := app.Dialog.OpenFile().SetTitle(tr("授权 Chrome 调试入口")).SetMessage(tr("请选择 DevToolsActivePort。ReadyRig 仅读取此文件中的本机调试地址；连接时仍需在 Chrome 中允许。")).SetButtonText(tr("授权读取")).SetDirectory(dir).ShowHiddenFiles(true).CanChooseFiles(true).CanChooseDirectories(false).PromptForSingleSelection()
			if err != nil {
				fail(tr("无法打开系统文件选择器"), 500)
				return
			}
			if chosen != "" && filepath.Clean(chosen) != filepath.Join(dir, "DevToolsActivePort") {
				fail(tr("请选择 Chrome 目录内的 DevToolsActivePort 文件"), 400)
				return
			}
			if chosen != "" {
				f, err := os.Open(chosen)
				if err != nil {
					fail(tr("macOS 仍拒绝读取调试入口。请检查系统设置 → 隐私与安全性中的 ReadyRig 数据访问权限，再重新检测。"), http.StatusForbidden)
					return
				}
				f.Close()
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": chosen != ""})
			return
		}
		handler.ServeHTTP(w, r)
	})
	app = application.New(application.Options{Name: "ReadyRig", Description: "Local Agent Adapter", Icon: brand.AppIcon(256), Assets: application.AssetOptions{Handler: native}, PostShutdown: func() { stop(); shutdown() }})
	window = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: tr("ReadyRig · 本地 Agent 控制台"), Width: 1320, Height: 860, MinWidth: 860, MinHeight: 600,
		URL: "/?shell=" + runtime.GOOS,
		Mac: application.MacWindow{TitleBar: application.MacTitleBarHiddenInset},
	})
	panel := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: tr("ReadyRig · 快捷面板"), Width: 700, Height: 640, MinWidth: 480, MinHeight: 400,
		URL: "/?shell=" + runtime.GOOS + "&panel=1", Hidden: true, Frameless: true, AlwaysOnTop: true, HideOnFocusLost: true, HideOnEscape: true,
		Mac: application.MacWindow{CornerRadius: 12},
	})
	showMain = func() { panel.Hide(); window.Show(); window.Focus() }
	menu := app.NewMenu()
	openItem := menu.Add(tr("打开 ReadyRig")).OnClick(func(*application.Context) { showMain() })
	statusItem := menu.Add(tr("已就绪")).SetEnabled(false)
	menu.AddSeparator()
	pauseItem := menu.Add(tr("暂停所有控制")).OnClick(func(*application.Context) { paused, _, _ := registry.Activity(); registry.SetPaused(!paused) })
	menu.AddSeparator()
	checkItem := menu.Add(tr("检查更新")).OnClick(func(*application.Context) { updates.Check(); showMain() })
	restartItem := menu.Add(tr("重启更新")).SetEnabled(false).OnClick(func(*application.Context) { _ = updates.RequestRestart() })
	menu.AddSeparator()
	quitItem := menu.Add(tr("退出 ReadyRig")).OnClick(func(*application.Context) { app.Quit() })
	tray := app.SystemTray.New()
	frames := make([][]byte, 11)
	for i := range frames {
		frames[i] = brand.Icon(44, i, false)
	}
	setIcon := func(frame int) {
		if runtime.GOOS == "darwin" {
			tray.SetTemplateIcon(frames[frame])
		} else {
			tray.SetIcon(frames[frame])
		}
	}
	setIcon(0)
	tray.SetTooltip(tr("ReadyRig · 本地 Agent 控制台"))
	tray.SetMenu(menu)
	tray.AttachWindow(panel).WindowOffset(6)
	applyLanguage = func() {
		window.SetTitle(tr("ReadyRig · 本地 Agent 控制台"))
		panel.SetTitle(tr("ReadyRig · 快捷面板"))
		tray.SetTooltip(tr("ReadyRig · 本地 Agent 控制台"))
		openItem.SetLabel(tr("打开 ReadyRig"))
		quitItem.SetLabel(tr("退出 ReadyRig"))
		checkItem.SetLabel(tr("检查更新"))
		paused, running, _ := registry.Activity()
		label := tr("已就绪")
		if paused {
			label = tr("控制已暂停")
		} else if running > 0 {
			label = tr("正在执行 %d 个任务", running)
		}
		statusItem.SetLabel(label)
		if paused {
			pauseItem.SetLabel(tr("恢复控制"))
		} else {
			pauseItem.SetLabel(tr("暂停所有控制"))
		}
		label = tr("重启更新")
		if status := updates.Status(); status.CanRestart {
			label += " · " + status.Latest
		}
		restartItem.SetLabel(label)
	}

	var wakeUntil atomic.Int64
	tray.OnClick(func() { wakeUntil.Store(time.Now().Add(time.Second).UnixNano()); tray.ToggleWindow() })
	// Left click toggles the attached panel; right click opens native actions.
	for _, w := range []*application.WebviewWindow{window, panel} {
		w.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) { w.Hide(); e.Cancel() })
	}
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) { showMain() })
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			ticker := time.NewTicker(125 * time.Millisecond)
			defer ticker.Stop()
			step, lastFrame := 0, -1
			for {
				select {
				case <-done:
					return
				case <-updates.RestartSignal():
					application.InvokeAsync(func() { app.Quit() })
					return
				case <-ticker.C:
					paused, running, lastStarted := registry.Activity()
					animationRunning := running
					if time.Now().UnixNano() < wakeUntil.Load() || time.Since(lastStarted) < 800*time.Millisecond {
						animationRunning++
					}
					frame := brand.AnimationFrame(paused, animationRunning, step, reduceMotion())
					step++
					if frame != lastFrame {
						setIcon(frame)
						lastFrame = frame
					}
					if step%8 != 0 {
						continue
					}
					status := updates.Status()
					label := tr("已就绪")
					if paused {
						label = tr("控制已暂停")
					} else if running > 0 {
						label = tr("正在执行 %d 个任务", running)
					}
					application.InvokeAsync(func() {
						select {
						case <-done:
							return
						default:
						}
						statusItem.SetLabel(label)
						pauseItem.SetLabel(map[bool]string{true: tr("恢复控制"), false: tr("暂停所有控制")}[paused])
						checkItem.SetEnabled(status.CanCheck && status.State != "checking" && status.State != "downloading")
						restartItem.SetEnabled(status.CanRestart)
						text := tr("重启更新")
						if status.CanRestart {
							text += " · " + status.Latest
						}
						restartItem.SetLabel(text)
					})
				}
			}
		}()
	})
	return app.Run()
}
