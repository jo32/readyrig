package main

import (
	"computer-use-server/internal/buildinfo"
	"computer-use-server/internal/cloud"
	"computer-use-server/internal/update"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

type startupOptions struct {
	Workspace, DataDir, Gateway, UI, Cloudflared, CloudURL, AllowIP           string
	ChromeURL, ChromeProfile, ChromeCommand, UpdateRepo, UpdateFeed           string
	FullAccess, Share, Shell, Computer, NoFiles, NoChrome, NoSafari, NoUpdate bool
	Foreground                                                                bool
}

const configFile = "cli.json"

func startupFlags(home, dataDir string, saved bool) (*flag.FlagSet, *startupOptions, error) {
	o := &startupOptions{}
	f := flag.NewFlagSet("readyrig", flag.ContinueOnError)
	f.Usage = func() { fmt.Fprint(f.Output(), cliHelp); f.PrintDefaults() }
	f.StringVar(&o.Workspace, "workspace", filepath.Join(home, "agent_workspace"), "Initial project directory")
	f.StringVar(&o.DataDir, "data-dir", dataDir, "Private application data directory (outside workspace)")
	f.StringVar(&o.Gateway, "gateway", "127.0.0.1:7332", "Agent API listener")
	f.StringVar(&o.UI, "ui", "127.0.0.1:7331", "Local dashboard and CLI control listener")
	f.BoolVar(&o.FullAccess, "full-access", false, "Allow file tools and command cwd outside approved projects for this run")
	f.BoolVar(&o.Share, "share", false, "Start a temporary Cloudflare tunnel on launch")
	f.StringVar(&o.Cloudflared, "cloudflared", "", "Override cloudflared executable")
	f.StringVar(&o.CloudURL, "cloud-url", buildinfo.CloudURL, "Cloud console URL for device binding")
	f.BoolVar(&o.Shell, "allow-shell", false, "Enable host shell execution (not sandboxed)")
	f.BoolVar(&o.Computer, "allow-computer", false, "Enable native computer use")
	f.BoolVar(&o.NoFiles, "no-files", false, "Disable project file tools")
	f.StringVar(&o.AllowIP, "allow-ip", "", "Comma-separated peer IP CIDRs for gateway")
	f.BoolVar(&o.NoChrome, "no-chrome", false, "Disable the Chrome browser tools")
	f.BoolVar(&o.NoSafari, "no-safari", false, "Disable the Safari MCP bridge (Safari 27+ on macOS)")
	f.StringVar(&o.ChromeURL, "chrome-browser-url", "", "Existing Chrome debugging HTTP URL on loopback")
	f.StringVar(&o.ChromeProfile, "chrome-user-data-dir", "", "Chrome profile containing DevToolsActivePort")
	f.StringVar(&o.ChromeCommand, "chrome-mcp-command", "", "Ignored: the Chrome tools are built in. Kept so that older command lines still start")
	f.StringVar(&o.UpdateRepo, "update-repo", buildinfo.ReleaseRepo, "GitHub release repository (owner/repo)")
	f.StringVar(&o.UpdateFeed, "update-feed", buildinfo.UpdateFeed, "Override release metadata URL")
	f.BoolVar(&o.NoUpdate, "no-update", false, "Disable release checks and automatic updates")
	f.BoolVar(&o.Foreground, "foreground", false, "Run serve/web in the foreground (for supervisors and debugging)")
	if saved {
		values, err := readConfig(dataDir)
		if err != nil {
			return nil, nil, err
		}
		for name, raw := range values {
			v := f.Lookup(name)
			if !persistentFlag(name) || v == nil {
				return nil, nil, fmt.Errorf("unknown saved setting %q", name)
			}
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, nil, err
			}
			switch v.Value.(flag.Getter).Get().(type) {
			case bool:
				if _, ok := value.(bool); !ok {
					return nil, nil, fmt.Errorf("saved setting %s must be a boolean", name)
				}
			case string:
				if _, ok := value.(string); !ok {
					return nil, nil, fmt.Errorf("saved setting %s must be a string", name)
				}
			}
			if err := f.Set(name, fmt.Sprint(value)); err != nil {
				return nil, nil, err
			}
		}
	}
	// Defaults < saved CLI settings < environment < explicit launch flags.
	for name, env := range map[string]string{"cloud-url": "CLOUD_URL", "update-repo": "UPDATE_REPO", "update-feed": "UPDATE_FEED", "no-update": "NO_UPDATE"} {
		if value := environment(env); value != "" {
			if name == "no-update" {
				value = fmt.Sprint(value == "1")
			}
			if err := f.Set(name, value); err != nil {
				return nil, nil, err
			}
		}
	}
	return f, o, nil
}

func persistentFlag(name string) bool {
	return name != "data-dir" && name != "full-access" && name != "foreground" && name != "if-needed"
}

func readConfig(dir string) (map[string]json.RawMessage, error) {
	b, err := os.ReadFile(filepath.Join(dir, configFile))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	var values map[string]json.RawMessage
	if err = json.Unmarshal(b, &values); err != nil || values == nil {
		return nil, fmt.Errorf("invalid CLI configuration in %s", filepath.Join(dir, configFile))
	}
	return values, nil
}

func saveConfig(dir string, f *flag.FlagSet) error {
	values := map[string]any{}
	f.VisitAll(func(v *flag.Flag) {
		if persistentFlag(v.Name) {
			values[v.Name] = v.Value.(flag.Getter).Get()
		}
	})
	return writePrivateJSON(filepath.Join(dir, configFile), values)
}

func capabilitySetting(category string, enabled bool) (string, bool) {
	switch category {
	case "files":
		return "no-files", !enabled
	case "terminal":
		return "allow-shell", enabled
	case "computer":
		return "allow-computer", enabled
	case "browser":
		return "no-chrome", !enabled
	case "safari":
		return "no-safari", !enabled
	default:
		return "", false
	}
}

// The running server owns the data lock and serializes capability saves. Merge
// only the chosen setting, preserving other saved options and launch overrides.
func saveCapabilityConfig(dir, category string, enabled bool) error {
	name, value := capabilitySetting(category, enabled)
	if name == "" {
		return errors.New("unknown capability")
	}
	values, err := readConfig(dir)
	if err != nil {
		return err
	}
	values[name], err = json.Marshal(value)
	if err != nil {
		return err
	}
	return writePrivateJSON(filepath.Join(dir, configFile), values)
}

// Replayed daemon/app launch flags must reflect the switches at shutdown.
func capabilityRestartArgs(args []string, flags *flag.FlagSet, enabled map[string]bool) []string {
	settings := map[string]bool{}
	for _, category := range []string{"files", "terminal", "computer", "browser", "safari"} {
		name, value := capabilitySetting(category, enabled[category])
		settings[name] = value
	}
	result := make([]string, 0, len(args)+len(settings))
	appendSettings := func() {
		for _, name := range []string{"no-files", "allow-shell", "allow-computer", "no-chrome", "no-safari"} {
			result = append(result, "--"+name+"="+fmt.Sprint(settings[name]))
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			appendSettings()
			return append(result, args[i:]...)
		}
		if strings.HasPrefix(arg, "-") {
			name, _, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
			if _, ok := settings[name]; ok {
				continue
			}
			// A string option's value may itself look like a capability flag.
			if option := flags.Lookup(name); option != nil && !inline && i+1 < len(args) {
				if _, isBool := option.Value.(flag.Getter).Get().(bool); !isBool {
					result = append(result, arg, args[i+1])
					i++
					continue
				}
			}
		}
		result = append(result, arg)
	}
	appendSettings()
	return result
}

func writePrivateJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cli-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func absolutePath(home, path string) (string, error) {
	if path == "~" {
		path = home
	} else if strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, path[2:])
	}
	if path == "" {
		return "", errors.New("directory path cannot be empty")
	}
	return filepath.Abs(path)
}

// This global option works before or after any subcommand.
func extractDataDir(args []string, home string) ([]string, string, error) {
	dir := defaultDataDir(home)
	remaining := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			remaining = append(remaining, args[i:]...)
			break
		}
		if arg == "--data-dir" || arg == "-data-dir" {
			i++
			if i == len(args) {
				return nil, "", errors.New("--data-dir requires a directory")
			}
			dir = args[i]
		} else if strings.HasPrefix(arg, "--data-dir=") || strings.HasPrefix(arg, "-data-dir=") {
			_, dir, _ = strings.Cut(arg, "=")
		} else {
			remaining = append(remaining, arg)
		}
	}
	dir, err := absolutePath(home, dir)
	return remaining, dir, err
}

func validateOptions(o *startupOptions) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	o.Workspace, err = absolutePath(home, o.Workspace)
	if err != nil {
		return err
	}
	// Resolve existing ancestors, including symlinks, before checking containment.
	workspace, err := resolveDirectory(o.Workspace)
	if err != nil {
		return err
	}
	data, err := resolveDirectory(o.DataDir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(workspace, data)
	if err == nil && filepath.IsLocal(rel) {
		return errors.New("data directory must be outside workspace")
	}
	for _, addr := range []string{o.Gateway, o.UI} {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("invalid listener %q: %w", addr, err)
		}
		if port == "" {
			return fmt.Errorf("listener %q needs a port", addr)
		}
		if _, err = net.LookupPort("tcp", port); err != nil {
			return err
		}
	}
	host, _, _ := net.SplitHostPort(o.UI)
	if host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return errors.New("dashboard must listen on loopback")
	}
	if o.CloudURL != "" {
		if _, err := cloud.ValidateURL(o.CloudURL); err != nil {
			return err
		}
	}
	if o.UpdateRepo != "" {
		if _, err := update.GitHubFeed(o.UpdateRepo); err != nil {
			return err
		}
	}
	if o.AllowIP != "" {
		for _, v := range strings.Split(o.AllowIP, ",") {
			if _, _, err := net.ParseCIDR(strings.TrimSpace(v)); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveDirectory(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err == nil {
		return real, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	real, err = resolveDirectory(parent)
	return filepath.Join(real, filepath.Base(path)), err
}

func printJSON(w io.Writer, value any) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(value)
}
