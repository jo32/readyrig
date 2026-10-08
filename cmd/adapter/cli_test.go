package main

import (
	"computer-use-server/internal/desktop"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("READYRIG_CLI_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"readyrig"}, os.Args[i+1:]...)
			if err := run(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestCLIServiceAndToolsEndToEnd(t *testing.T) {
	if desktop.Available {
		t.Skip("use -tags nogui for the CLI process test")
	}
	home := t.TempDir()
	data, workspace := filepath.Join(home, "private data"), filepath.Join(home, "work space")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := func(args ...string) *exec.Cmd {
		cmd := exec.Command(executable, append([]string{"-test.run=^TestCLIHelperProcess$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "READYRIG_CLI_TEST_PROCESS=1", "HOME="+home,
			"READYRIG_CLOUD_URL=", "READYRIG_UPDATE_REPO=", "READYRIG_UPDATE_FEED=", "READYRIG_NO_UPDATE=1")
		return cmd
	}
	cli := func(success bool, args ...string) []byte {
		t.Helper()
		cmd := command(append(args, "--data-dir", data)...)
		out, err := cmd.CombinedOutput()
		if (err == nil) != success {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}
	cli(true, "init", "--workspace", workspace, "--gateway", "127.0.0.1:0", "--ui", "127.0.0.1:0", "--allow-shell", "--no-chrome", "--cloud-url", "")
	for _, path := range []string{filepath.Join(data, configFile)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private file permissions: %v %v", info, err)
		}
	}
	config := cli(true, "config", "show")
	if strings.Contains(string(config), "full-access") {
		t.Fatal("Full Access was persisted")
	}

	start := func(extra ...string) (*exec.Cmd, func()) {
		t.Helper()
		args := append([]string{"--data-dir", data}, extra...)
		cmd := command(args...)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			_ = cmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("service exit: %v", err)
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("service did not stop")
			}
		}
		t.Cleanup(stop)
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := newControlClient(data); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("service did not become ready")
			}
			time.Sleep(20 * time.Millisecond)
		}
		return cmd, stop
	}
	_, stop := start("serve", "--foreground")
	info, err := os.Stat(filepath.Join(data, "control.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("control metadata is not private", err)
	}
	metadata, err := os.ReadFile(filepath.Join(data, "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	var endpoint controlEndpoint
	if err := json.Unmarshal(metadata, &endpoint); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), "key") {
		t.Fatal("control file contains an admin key")
	}
	socketInfo, err := os.Stat(endpoint.Socket)
	if err != nil || socketInfo.Mode().Perm() != 0600 {
		t.Fatal("control socket is not private", err)
	}
	client, err := newControlClient(data)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, "http://readyrig.local/api/pause", strings.NewReader(`{"paused":true}`))
	request.Header.Set("Origin", "http://evil.example")
	response, err := client.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("browser origin accepted on the CLI socket")
	}
	var state struct {
		Enabled  map[string]bool `json:"enabled"`
		Paused   bool            `json:"paused"`
		LocalCLI struct {
			Command string `json:"command"`
			DataDir string `json:"data_dir"`
			Mode    string `json:"mode"`
		} `json:"local_cli"`
	}
	if err := json.Unmarshal(cli(true, "status"), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Enabled["terminal"] || state.Enabled["browser"] {
		t.Fatal("startup settings ignored", state.Enabled)
	}
	if state.LocalCLI.Command != executable || state.LocalCLI.DataDir != data || state.LocalCLI.Mode != "foreground" {
		t.Fatal("configuration prompt would address the wrong instance", state.LocalCLI)
	}
	cli(false, "config", "set", "allow-shell", "false") // Cannot race the running app.
	cli(true, "tools")
	cli(true, "connection")
	cli(true, "call", "--session", "task-1", "write_file", `{"path":"hello.txt","content":"from CLI"}`)
	if content, err := os.ReadFile(filepath.Join(workspace, "hello.txt")); err != nil || string(content) != "from CLI" {
		t.Fatal("tool did not execute", err)
	}
	cli(true, "call", "--session", "task-1", "exec_command", `{"command":"pwd"}`)
	cli(true, "capability", "terminal", "off")
	denied := cli(false, "call", "exec_command", `{"command":"touch forbidden"}`)
	if !strings.Contains(string(denied), `"status": "denied"`) {
		t.Fatal("failure did not include JSON", string(denied))
	}
	if _, err := os.Stat(filepath.Join(workspace, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("disabled tool executed")
	}
	cli(true, "pause")
	cli(false, "call", "write_file", `{"path":"paused.txt","content":"blocked"}`)
	cli(true, "resume")

	project := filepath.Join(home, "another project")
	if err := os.Mkdir(project, 0755); err != nil {
		t.Fatal(err)
	}
	cli(true, "projects", "add", project)
	cli(true, "projects", "list")
	cli(true, "share", "status")
	cli(true, "share", "stop")
	cli(true, "cloud", "status")
	cli(true, "privacy", "on")
	cli(true, "privacy", "words", "TEAM=acme-corp")
	cli(false, "privacy", "words", "bad")
	cli(true, "privacy", "status")
	cli(true, "privacy", "off")
	cli(true, "capability", "computer", "on")
	cli(true, "capability", "files", "off")
	stop()
	if _, err := os.Stat(filepath.Join(data, "control.json")); !os.IsNotExist(err) {
		t.Fatal("control metadata not removed on exit")
	}
	cli(false, "status")
	_, stop = start("serve", "--foreground")
	if err := json.Unmarshal(cli(true, "status"), &state); err != nil || !state.Enabled["computer"] || state.Enabled["files"] || state.Enabled["terminal"] || state.Enabled["browser"] {
		t.Fatal("last capability choices did not survive restart", state.Enabled, err)
	}
	cli(true, "capability", "computer", "off")
	cli(true, "capability", "files", "on")
	stop()
	cli(true, "config", "set", "allow-shell", "false")
	_, stop = start("serve", "--foreground", "--allow-shell", "--full-access") // Explicit launch override, never saved.
	if err := json.Unmarshal(cli(true, "status"), &state); err != nil || !state.Enabled["terminal"] {
		t.Fatal("launch override ignored", err)
	}
	var projects struct {
		Projects []struct {
			Path string `json:"path"`
		} `json:"projects"`
		FullAccess bool `json:"full_access"`
	}
	if err := json.Unmarshal(cli(true, "projects", "list"), &projects); err != nil || len(projects.Projects) != 2 || !projects.FullAccess {
		t.Fatal("project persistence or access override failed", err)
	}
	stop()
	_, stop = start("web", "--foreground")
	if err := json.Unmarshal(cli(true, "projects", "list"), &projects); err != nil || projects.FullAccess {
		t.Fatal("Full Access survived a restart", err)
	}
	if err := json.Unmarshal(cli(true, "status"), &state); err != nil || state.Enabled["terminal"] || state.Enabled["computer"] || !state.Enabled["files"] {
		t.Fatal("saved capability settings ignored", state.Enabled, err)
	}
	stop()
}

func TestDesktopReadsSavedStartupSettings(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "private")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	// Invalid persisted settings must be detected before opening a window or
	// starting tools. This also works with nogui process tests.
	if err := writePrivateJSON(filepath.Join(data, configFile), map[string]any{"workspace": home}); err != nil {
		t.Fatal(err)
	}
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	os.Args = []string{"readyrig", "desktop", "--data-dir", data}
	t.Setenv("HOME", home)
	if err := run(); err == nil || !strings.Contains(err.Error(), "data directory must be outside workspace") {
		t.Fatal("desktop ignored the CLI startup configuration", err)
	}
}

func TestLocalCLIContextUsesMatchingAppHelper(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "ReadyRig.app", "Contents")
	executable := filepath.Join(bundle, "MacOS", "readyrig")
	helper := filepath.Join(bundle, "Helpers", "readyrig")
	if err := os.MkdirAll(filepath.Dir(helper), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	context := localCLIContext(executable, "/custom/data", "desktop")
	if context.Command != helper || context.DataDir != "/custom/data" || context.Mode != "desktop" {
		t.Fatal("configuration prompt lost the matching bundled CLI", context)
	}
	if err := os.Chmod(helper, 0600); err != nil {
		t.Fatal(err)
	}
	if context := localCLIContext(executable, "/custom/data", "desktop"); context.Command != executable {
		t.Fatal("prompt references a non-executable helper", context)
	}
}

func TestControlClientStaysOnPrivateSocket(t *testing.T) {
	leaked := false
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer receiver.Close()
	dir, err := os.MkdirTemp("", "rr-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	local := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, receiver.URL, 302) })}
	go local.Serve(ln)
	defer local.Close()
	if err := writePrivateJSON(filepath.Join(dir, "control.json"), controlEndpoint{Socket: socket}); err != nil {
		t.Fatal(err)
	}
	if _, err := newControlClient(dir); err == nil {
		t.Fatal("redirect accepted")
	}
	if leaked {
		t.Fatal("request forwarded to TCP")
	}
	for _, endpoint := range []any{map[string]string{"socket": "relative.sock"}, map[string]string{"socket": socket, "url": receiver.URL}, map[string]string{"socket": filepath.Join(dir, "control.json")}, map[string]string{"url": receiver.URL, "key": "private-key"}} {
		if err := writePrivateJSON(filepath.Join(dir, "control.json"), endpoint); err != nil {
			t.Fatal(err)
		}
		if _, err := newControlClient(dir); err == nil {
			t.Fatal("unsafe endpoint accepted", endpoint)
		}
	}
}

func TestConfigurationRejectsUnsafeOrInvalidValues(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "private")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	for _, values := range []any{map[string]any{"full-access": true}, map[string]any{"allow-shell": "true"}, map[string]any{"unknown": "setting"}, []any{}, nil} {
		if err := writePrivateJSON(filepath.Join(data, configFile), values); err != nil {
			t.Fatal(err)
		}
		if _, _, err := startupFlags(home, data, true); err == nil {
			t.Fatal("invalid saved settings accepted", values)
		}
	}
	if err := os.Remove(filepath.Join(data, configFile)); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--workspace", home}, {"--ui", "0.0.0.0:7331"}, {"--allow-ip", "invalid"}, {"--gateway", "missing-port"}} {
		flags, options, err := startupFlags(home, data, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := flags.Parse(args); err != nil {
			t.Fatal(err)
		}
		if err := validateOptions(options); err == nil {
			t.Fatal("invalid options accepted", args)
		}
	}
	link := filepath.Join(home, "link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	flags, options, err := startupFlags(home, data, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := flags.Set("workspace", link); err != nil {
		t.Fatal(err)
	}
	if err := validateOptions(options); err == nil {
		t.Fatal("symlink containment bypassed validation")
	}
}

func TestServiceUnitEscapesPaths(t *testing.T) {
	unit, err := serviceUnit(`/home/user/a "$quote"/readyrig`, `/home/user/100%/$data`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unit, `ExecStart=:"/home/user/a \"$quote\"/readyrig" serve --foreground --data-dir "/home/user/100%%/$data"`) {
		t.Fatal(unit)
	}
	if _, err := serviceUnit("/tmp/readyrig\nInjected=true", "/tmp/data"); err == nil {
		t.Fatal("multiline service path accepted")
	}
}
