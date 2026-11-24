package chromemcp

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestModernChromeWithoutHTTPDiscovery(t *testing.T) {
	// Chrome's permission-based debugging server deliberately returns 404 here.
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	u, _ := url.Parse(server.URL)
	dir := t.TempDir()
	binary := filepath.Join(dir, "chrome")
	if err := os.WriteFile(binary, []byte("installed"), 0700); err != nil {
		t.Fatal(err)
	}
	reader := func(string) ([]byte, error) {
		return []byte(u.Port() + "\n/devtools/browser/permission-session\n"), nil
	}
	for _, browserURL := range []string{"", server.URL} {
		got, err := discoverWithReader(context.Background(), Options{UserDataDir: dir, BrowserURL: browserURL}, []string{binary}, nil, reader)
		expected := "ws://" + u.Host + "/devtools/browser/permission-session"
		if err != nil || got.Endpoint != expected {
			t.Fatalf("got %+v, %v", got, err)
		}
	}
	// An explicit port must not silently attach to another Chrome instance.
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	if _, err := discoverWithReader(context.Background(), Options{UserDataDir: dir, BrowserURL: other.URL}, []string{binary}, nil, reader); err == nil {
		t.Fatal("accepted different browser port")
	}
}

func TestApprovalServerConnectsWithoutReadingProfile(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		if r.URL.Path == "/json/version" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path != "/devtools/readyrig-probe" {
			t.Errorf("discovery requested a path that could prompt for approval: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		nonce, err := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key"))
		if r.Header.Get("Connection") != "Upgrade" || r.Header.Get("Upgrade") != "websocket" || r.Header.Get("Sec-WebSocket-Version") != "13" || err != nil || len(nonce) != 16 {
			t.Error("invalid WebSocket probe")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("Connection rejected"))
	}))
	defer server.Close()
	dir := t.TempDir()
	binary := filepath.Join(dir, "chrome")
	if err := os.WriteFile(binary, []byte("installed"), 0700); err != nil {
		t.Fatal(err)
	}
	reader := func(string) ([]byte, error) {
		t.Error("read Chrome's profile despite an available approval server")
		return nil, os.ErrPermission
	}
	got, err := discoverWithReader(context.Background(), Options{BrowserURL: server.URL}, []string{binary}, []string{dir}, reader)
	expected := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser"
	if err != nil || got.Endpoint != expected {
		t.Fatalf("got %+v, %v", got, err)
	}
	if len(requests) != 2 || requests[0] != "/json/version" || requests[1] != "/devtools/readyrig-probe" {
		t.Fatalf("unexpected discovery requests: %v", requests)
	}
}

func TestApprovalProbeRejectsOtherServers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unrelated 404", http.StatusNotFound, ""},
		{"unrelated 403", http.StatusForbidden, "Forbidden"},
		{"redirect", http.StatusFound, "Connection rejected"},
		{"unexpected upgrade", http.StatusSwitchingProtocols, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/json/version" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if tc.status == http.StatusFound {
					w.Header().Set("Location", "http://example.com")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			if _, err := probeURL(context.Background(), server.URL); err == nil {
				t.Fatal("accepted an unrelated server")
			}
		})
	}
}

func TestDebugPermissionIsActionable(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "chrome")
	if err := os.WriteFile(binary, []byte("installed"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, cause := range []error{os.ErrPermission, syscall.EPERM, syscall.EACCES} {
		reader := func(path string) ([]byte, error) { return nil, &os.PathError{Op: "open", Path: path, Err: cause} }
		_, err := discoverWithReader(context.Background(), Options{UserDataDir: dir}, []string{binary}, nil, reader)
		if !errors.Is(err, ErrDebugPermission) {
			t.Fatalf("permission confused with debugging disabled: %v", err)
		}
	}
	reader := func(path string) ([]byte, error) {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
	}
	_, err := discoverWithReader(context.Background(), Options{UserDataDir: dir}, []string{binary}, nil, reader)
	if errors.Is(err, ErrDebugPermission) || err == nil || !strings.Contains(err.Error(), "开启远程调试") {
		t.Fatalf("missing file confused with permission: %v", err)
	}
}
