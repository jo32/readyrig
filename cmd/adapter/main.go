package main

import (
	"computer-use-server/internal/app"
	"computer-use-server/internal/buildinfo"
	"computer-use-server/internal/chromemcp"
	"computer-use-server/internal/cliinstall"
	"computer-use-server/internal/desktop"
	"computer-use-server/internal/harness"
	"computer-use-server/internal/server"
	"computer-use-server/internal/tunnel"
	"computer-use-server/internal/update"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil && err != flag.ErrHelp {
		log.Fatal(err)
	}
}
func run() (runErr error) {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("ReadyRig", buildinfo.Version)
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	args, dataDir, err := extractDataDir(os.Args[1:], home)
	if err != nil {
		return err
	}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("ReadyRig", buildinfo.Version)
		return nil
	}
	mode := "desktop"
	if !desktop.Available {
		mode = "tui"
		if runtime.GOOS == "windows" {
			mode = "web"
		}
	}
	explicitCommand := len(args) > 0 && !strings.HasPrefix(args[0], "-")
	if explicitCommand {
		mode, args = args[0], args[1:]
	}
	if mode == "serve" {
		mode = "web"
	}
	// Startup flags without a command retain their existing launch behavior.
	if mode == "tui" && !explicitCommand && len(args) > 0 && args[0] != "--help" && args[0] != "-h" {
		mode = "web"
	}
	flags, opts, err := startupFlags(home, dataDir, mode != "help" && mode != "version")
	if err != nil {
		return err
	}
	if handled, err := manageCLI(mode, args, flags, opts); handled {
		return err
	}
	if mode == "setup" {
		flags.Bool("if-needed", false, "Skip the guide when startup settings already exist")
	}
	if err = flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if mode != "desktop" && mode != "web" && mode != "update" && mode != "init" && mode != "setup" && mode != "tui" && mode != "stop" && mode != "restart" {
		return fmt.Errorf("unknown command %q; see 'readyrig help'", mode)
	}
	if mode == "tui" {
		return runTUI(flags, opts)
	}
	if mode == "setup" {
		return runSetup(flags, opts)
	}
	if mode == "stop" {
		return stopDaemon(opts.DataDir, os.Stdout)
	}
	if err := validateOptions(opts); err != nil {
		return err
	}
	if mode == "init" {
		if opts.FullAccess {
			return fmt.Errorf("Full Access is session-only; use 'serve --full-access'")
		}
		unlock, err := configurationLock(opts.DataDir)
		if err != nil {
			return err
		}
		defer unlock()
		if err := os.MkdirAll(opts.Workspace, 0755); err != nil {
			return err
		}
		if err := saveConfig(opts.DataDir, flags); err != nil {
			return err
		}
		fmt.Println("Saved CLI configuration:", filepath.Join(opts.DataDir, configFile))
		fmt.Println("Start ReadyRig with: readyrig serve --data-dir", opts.DataDir)
		return nil
	}
	if mode == "restart" {
		if err := stopDaemon(opts.DataDir, os.Stdout); err != nil {
			return err
		}
		return startDaemon(flags, opts, os.Stdout)
	}
	if mode == "web" && !opts.Foreground && runtime.GOOS != "windows" {
		return startDaemon(flags, opts, os.Stdout)
	}
	feed := opts.UpdateFeed
	if feed == "" && opts.UpdateRepo != "" {
		feed, err = update.GitHubFeed(opts.UpdateRepo)
		if err != nil {
			return err
		}
	}
	var runningServer *server.Server
	updates := update.New(update.Options{Version: buildinfo.Version, Feed: feed, GUI: desktop.Available, Disabled: opts.NoUpdate, RestartArgs: func() []string {
		if runningServer == nil {
			return os.Args[1:]
		}
		_, enabled := runningServer.Registry.State()
		return capabilityRestartArgs(os.Args[1:], flags, enabled)
	}})
	// Registered before App.Close and listener shutdown: install only after all
	// tools, data files and ports have been released.
	defer func() {
		if err := updates.Finish(runErr == nil); err != nil {
			runErr = err
		}
	}()
	if mode == "update" {
		updates.Check()
		for {
			s := updates.Status()
			if s.State != "checking" && s.State != "downloading" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		s := updates.Status()
		if s.Error != "" {
			return fmt.Errorf("update: %s", s.Error)
		}
		if s.State == "ready" {
			if err := updates.Finish(true); err != nil {
				return err
			}
			fmt.Println("Updated ReadyRig to", s.Latest)
			return nil
		}
		if s.State == "latest" {
			fmt.Println("ReadyRig", s.Current, "is up to date")
			return nil
		}
		return fmt.Errorf("update: %s", s.Reason)
	}
	a, err := app.New(opts.Workspace, opts.DataDir)
	if err != nil {
		return err
	}
	closeApp := sync.OnceFunc(a.Close)
	defer closeApp()
	runningServer = a.Server
	a.Server.SaveCapability = func(category string, enabled bool) error {
		return saveCapabilityConfig(a.Server.Store.Dir, category, enabled)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		a.Server.LocalCLI = localCLIContext(executable, opts.DataDir, mode)
	}
	if mode == "desktop" && runtime.GOOS == "darwin" {
		status := cliinstall.Install(cliinstall.Options{Executable: executable, Home: home, Shell: os.Getenv("SHELL"), ZDotDir: os.Getenv("ZDOTDIR"), ConfigHome: os.Getenv("XDG_CONFIG_HOME")})
		if status.State != "" {
			a.Server.CLI = &status
		}
		if status.State == "error" {
			log.Printf("CLI installation: %s", status.Error)
		}
	}
	a.Server.Updates = updates
	if opts.Cloudflared != "" {
		a.Server.Tunnel = tunnel.New(tunnel.Options{Dir: filepath.Join(a.Server.Store.Dir, "cloudflared"), Command: opts.Cloudflared, Changed: a.Server.Registry.Signal, RedactSecrets: a.Server.Registry.AddSecrets})
		a.Transfers.NewTunnel = func() harness.PublicTunnel {
			return tunnel.New(tunnel.Options{Dir: filepath.Join(a.Server.Store.Dir, "cloudflared"), Command: opts.Cloudflared, Changed: a.Server.Registry.Signal})
		}
	}
	a.Server.Projects.SetFullAccess(opts.FullAccess)
	for category, enabled := range map[string]bool{"files": !opts.NoFiles, "terminal": opts.Shell, "computer": opts.Computer, "browser": !opts.NoChrome, "safari": !opts.NoSafari} {
		if err := a.Server.Registry.Enable(category, enabled); err != nil {
			return err
		}
	}
	if err = a.Chrome.Start(chromemcp.Options{Disabled: opts.NoChrome, BrowserURL: opts.ChromeURL, UserDataDir: opts.ChromeProfile, Command: opts.ChromeCommand}); err != nil {
		return err
	}
	a.Safari.Start(chromemcp.SafariOptions{Disabled: opts.NoSafari})
	if opts.AllowIP != "" {
		for _, v := range strings.Split(opts.AllowIP, ",") {
			_, cidr, e := net.ParseCIDR(strings.TrimSpace(v))
			if e != nil {
				return e
			}
			a.Server.AllowedIPs = append(a.Server.AllowedIPs, cidr)
		}
	}
	gw, ln, err := server.Listen(opts.Gateway, a.Server.Gateway())
	if err != nil {
		return err
	}
	closeGateway := sync.OnceFunc(func() { server.Shutdown(gw) })
	defer closeGateway()
	a.Server.GatewayAddr = "http://" + ln.Addr().String()
	if err = a.Server.StartCloud(opts.CloudURL); err != nil {
		return err
	}
	fmt.Println("ReadyRig Agent API:", a.Server.GatewayURL())
	fmt.Println("Workspace:", a.Server.Workspace)
	if opts.Share {
		if err = a.Server.StartSharing(); err != nil {
			return err
		}
		go func() {
			for {
				status := a.Server.Tunnel.Status()
				if status.State == "ready" {
					fmt.Println("Public Agent API:", status.URL+"/"+a.Server.AccessPath)
					fmt.Println("Public console:", status.URL+"/"+a.Server.AccessPath+"/app/")
					return
				}
				if status.State == "error" {
					log.Printf("Public sharing: %s", status.Error)
					return
				}
				if status.State == "stopped" {
					return
				}
				time.Sleep(200 * time.Millisecond)
			}
		}()
	}
	quit := make(chan struct{})
	requestQuit := sync.OnceFunc(func() { close(quit) })
	if mode == "desktop" {
		closeControl, err := startLocalControl(a.Server, runtimeInfo{PID: os.Getpid(), Mode: "desktop"}, nil)
		if err != nil {
			return err
		}
		defer closeControl()
		updates.Start()
		return desktop.Run(a.Server.UI(), a.Server.Registry, updates, func() {
			// AppKit can terminate without running Go defers.
			closeControl()
			closeGateway()
			closeApp()
			if err := updates.Finish(true); err != nil {
				log.Printf("update: %v", err)
			}
		}, filepath.Join(opts.DataDir, "language.json"))
	}
	web, uiln, err := server.Listen(opts.UI, a.Server.BrowserGuard(a.Server.UI()))
	if err != nil {
		return err
	}
	defer server.Shutdown(web)
	dashboard := fmt.Sprintf("http://%s/#key=%s", uiln.Addr(), a.Server.UIKey)
	fmt.Println("Dashboard:", dashboard)
	runMode := "foreground"
	if os.Getenv("READYRIG_DAEMON_CHILD") == "1" {
		runMode = "daemon"
	}
	// Publish the local control endpoint only after both listeners are ready.
	closeControl, err := startLocalControl(a.Server, runtimeInfo{PID: os.Getpid(), Mode: runMode, Dashboard: dashboard}, requestQuit)
	if err != nil {
		return err
	}
	defer closeControl()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	updates.Start()
	select {
	case <-stop:
	case <-quit:
	case <-updates.RestartSignal():
	}
	return nil
}

func localCLIContext(executable, dataDir, mode string) *server.LocalCLI {
	command := executable
	// Use the helper matching this app, even when an independent CLI is on PATH.
	if helper, bundled := cliinstall.HelperPath(executable); bundled {
		if info, err := os.Stat(helper); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			command = helper
		}
	}
	if mode == "web" {
		mode = "foreground"
		if os.Getenv("READYRIG_DAEMON_CHILD") == "1" {
			mode = "daemon"
		}
	}
	return &server.LocalCLI{Command: command, DataDir: dataDir, Mode: mode}
}

// Reuse existing installations without moving their private logs or projects.
func defaultDataDir(home string) string {
	current := filepath.Join(home, ".local", "share", "readyrig")
	if _, err := os.Stat(current); os.IsNotExist(err) {
		// Preserve both earlier names without moving logs, projects or secrets.
		for _, name := range []string{"readrig", "relay"} {
			legacy := filepath.Join(home, ".local", "share", name)
			if info, err := os.Stat(legacy); err == nil && info.IsDir() {
				return legacy
			}
		}
	}
	return current
}

// Prefer the current name while keeping existing launch configurations usable.
func environment(name string) string {
	if value, ok := os.LookupEnv("READYRIG_" + name); ok {
		return value
	}
	return os.Getenv("RELAY_" + name)
}
