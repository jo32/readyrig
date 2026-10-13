package harness

import (
	"bytes"
	"computer-use-server/internal/tunnel"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTunnel publishes the local listener itself as the public address.
type fakeTunnel struct {
	mu            sync.Mutex
	url           string
	starts, stops int
}

func (f *fakeTunnel) Start(target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.url = target
	f.starts++
	return nil
}
func (f *fakeTunnel) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.url = ""
	f.stops++
	return nil
}
func (f *fakeTunnel) Status() tunnel.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.url == "" {
		return tunnel.Status{State: "stopped"}
	}
	return tunnel.Status{State: "ready", URL: f.url}
}
func (f *fakeTunnel) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

func transfersForTest(t *testing.T) (*Registry, *Transfers, *fakeTunnel, string) {
	t.Helper()
	r, f, root := filesForTest(t)
	ft := &fakeTunnel{}
	tr := NewTransfers(f)
	tr.NewTunnel = func() PublicTunnel { return ft }
	tr.AllowURL = func(u *url.URL) bool { return u.Scheme == "http" && u.Hostname() == "127.0.0.1" }
	tr.Chunk = 64 << 10
	tr.Grace = 50 * time.Millisecond
	tr.Register(r)
	t.Cleanup(tr.Close)
	return r, tr, ft, root
}

func randomFile(t *testing.T, root, name string, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	rand.Read(b)
	put(t, root, name, string(b))
	return b
}

func sumHex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func openUpload(t *testing.T, r *Registry, args map[string]any) string {
	t.Helper()
	out, err := invoke(t, r, "receive_file", args)
	if err != nil {
		t.Fatal(err)
	}
	link, _ := asMap(t, out)["upload_url"].(string)
	if link == "" {
		t.Fatalf("no upload_url: %v", out.Value)
	}
	return link
}

// upload sends one raw request and returns the status and Upload-Offset.
func upload(t *testing.T, method, link string, offset int64, body []byte) (int, int64) {
	t.Helper()
	req, _ := http.NewRequest(method, link, bytes.NewReader(body))
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	got, _ := strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
	return resp.StatusCode, got
}

func parts(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*"+partSuffix+"*"))
	return m
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for " + what)
		}
	}
}

func TestSendFileDeliversThroughReceiveFile(t *testing.T) {
	r, _, ft, root := transfersForTest(t)
	data := randomFile(t, root, "src.bin", 300<<10)
	link := openUpload(t, r, map[string]any{"path": "in/dst.bin", "size": len(data)})
	out, err := invoke(t, r, "send_file", map[string]any{"path": "src.bin", "upload_url": link, "yield_time_ms": 10000})
	if err != nil {
		t.Fatal(err)
	}
	if v := asMap(t, out); v["done"] != true || v["sha256"] != sumHex(data) {
		t.Fatalf("send result: %v", v)
	}
	r.WaitBackground()
	got, err := os.ReadFile(filepath.Join(root, "in", "dst.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("delivered file differs: %v", err)
	}
	if p := parts(t, filepath.Join(root, "in")); len(p) != 0 {
		t.Fatalf("staging files left: %v", p)
	}
	var finished bool
	for _, n := range r.TakeNotices("test-session") {
		finished = finished || n.Kind == "task_finished" && n.Data["tool"] == "receive_file" && n.Data["status"] == "success"
	}
	if !finished {
		t.Fatal("receive_file did not report success")
	}
	// A finished upload still confirms itself to a sender that lost the reply.
	if code, off := upload(t, http.MethodHead, link, 0, nil); code != 200 || off != int64(len(data)) {
		t.Fatalf("HEAD after finish: %d %d", code, off)
	}
	eventually(t, "the tunnel to close after the grace period", func() bool { _, stops := ft.counts(); return stops == 1 })
}

func TestUploadResumesAndRejectsGaps(t *testing.T) {
	r, _, _, root := transfersForTest(t)
	data := randomFile(t, root, "src.bin", 200<<10)
	link := openUpload(t, r, map[string]any{"path": "dst.bin", "size": len(data), "sha256": sumHex(data)})
	if code, off := upload(t, http.MethodPatch, link, 0, data[:1000]); code != 204 || off != 1000 {
		t.Fatalf("first piece: %d %d", code, off)
	}
	if code, off := upload(t, http.MethodPatch, link, 5000, data[5000:6000]); code != 409 || off != 1000 {
		t.Fatalf("a gap must be refused with the real offset: %d %d", code, off)
	}
	// Resending from an earlier offset replaces what follows it.
	if code, off := upload(t, http.MethodPatch, link, 500, data[500:700]); code != 204 || off != 700 {
		t.Fatalf("replayed piece: %d %d", code, off)
	}
	if code, off := upload(t, http.MethodPatch, link, 700, make([]byte, len(data))); code != 413 || off != 0 {
		t.Fatalf("oversized piece: %d %d", code, off)
	}
	out, err := invoke(t, r, "send_file", map[string]any{"path": "src.bin", "upload_url": link, "yield_time_ms": 10000})
	if err != nil || asMap(t, out)["done"] != true {
		t.Fatalf("resume: %v %v", out.Value, err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "dst.bin")); !bytes.Equal(got, data) {
		t.Fatal("resumed file differs")
	}
}

func TestHashMismatchDiscardsTheFile(t *testing.T) {
	r, _, _, root := transfersForTest(t)
	data := randomFile(t, root, "src.bin", 10<<10)
	link := openUpload(t, r, map[string]any{"path": "dst.bin", "size": len(data), "sha256": sumHex([]byte("something else"))})
	_, err := invoke(t, r, "send_file", map[string]any{"path": "src.bin", "upload_url": link, "yield_time_ms": 10000})
	if ErrorCode(err) != "upload_rejected" || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("want a rejected upload, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "dst.bin")); !os.IsNotExist(err) {
		t.Fatal("a file that failed its check was put in place")
	}
	if p := parts(t, root); len(p) != 0 {
		t.Fatalf("staging files left: %v", p)
	}
}

func TestReceiveRefusesExistingFilesUnlessOverwrite(t *testing.T) {
	r, _, _, root := transfersForTest(t)
	put(t, root, "dst.bin", "old")
	if _, err := invoke(t, r, "receive_file", map[string]any{"path": "dst.bin", "size": 3}); ErrorCode(err) != "file_exists" {
		t.Fatalf("want file_exists, got %v", err)
	}
	link := openUpload(t, r, map[string]any{"path": "dst.bin", "size": 3, "overwrite": true})
	if code, _ := upload(t, http.MethodPatch, link, 0, []byte("new")); code != 204 {
		t.Fatalf("overwrite upload: %d", code)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "dst.bin")); string(got) != "new" {
		t.Fatalf("content %q", got)
	}
}

func TestUploadsShareOneTunnelAndReuseAddresses(t *testing.T) {
	r, _, ft, _ := transfersForTest(t)
	a := openUpload(t, r, map[string]any{"path": "a.bin", "size": 10})
	again := openUpload(t, r, map[string]any{"path": "a.bin", "size": 10})
	b := openUpload(t, r, map[string]any{"path": "b.bin", "size": 10})
	if a != again {
		t.Fatalf("same path, new address: %s %s", a, again)
	}
	ua, _ := url.Parse(a)
	ub, _ := url.Parse(b)
	if a == b || ua.Host != ub.Host {
		t.Fatalf("addresses should differ on one host: %s %s", a, b)
	}
	if starts, _ := ft.counts(); starts != 1 {
		t.Fatalf("tunnel started %d times", starts)
	}
	// A different size for the same path replaces the upload.
	if replaced := openUpload(t, r, map[string]any{"path": "a.bin", "size": 11}); replaced == a {
		t.Fatal("a different file kept the old address")
	}
	if code, _ := upload(t, http.MethodHead, a, 0, nil); code != 404 {
		t.Fatalf("replaced address still open: %d", code)
	}
}

func TestCancelClosesTheAddressAndDiscards(t *testing.T) {
	r, _, _, root := transfersForTest(t)
	link := openUpload(t, r, map[string]any{"path": "dst.bin", "size": 100})
	upload(t, http.MethodPatch, link, 0, make([]byte, 40))
	if _, err := invoke(t, r, "receive_file", map[string]any{"path": "dst.bin", "cancel": true}); err != nil {
		t.Fatal(err)
	}
	if code, _ := upload(t, http.MethodHead, link, 0, nil); code != 404 {
		t.Fatalf("cancelled address answers %d", code)
	}
	if p := parts(t, root); len(p) != 0 {
		t.Fatalf("staging files left: %v", p)
	}
}

func TestPauseClosesAddressesButKeepsWhatArrived(t *testing.T) {
	r, _, _, _ := transfersForTest(t)
	link := openUpload(t, r, map[string]any{"path": "dst.bin", "size": 100})
	upload(t, http.MethodPatch, link, 0, make([]byte, 40))
	r.SetPaused(true)
	eventually(t, "pause to close the address", func() bool { code, _ := upload(t, http.MethodHead, link, 0, nil); return code == 404 })
	r.SetPaused(false)
	out, err := invoke(t, r, "receive_file", map[string]any{"path": "dst.bin", "size": 100})
	if err != nil || asMap(t, out)["received"] != int64(40) {
		t.Fatalf("resume after pause: %v %v", out.Value, err)
	}
}

func TestIdleAddressesExpire(t *testing.T) {
	r, tr, _, _ := transfersForTest(t)
	tr.Idle = 50 * time.Millisecond
	link := openUpload(t, r, map[string]any{"path": "dst.bin", "size": 100})
	eventually(t, "the address to expire", func() bool { code, _ := upload(t, http.MethodHead, link, 0, nil); return code == 404 })
}

func TestReceiveLimitsAndArguments(t *testing.T) {
	r, _, _, _ := transfersForTest(t)
	if _, err := invoke(t, r, "receive_file", map[string]any{"path": "big.bin", "size": MaxTransferBytes + 1}); ErrorCode(err) != "invalid_arguments" {
		t.Fatalf("over 1 GiB: %v", err)
	}
	if _, err := invoke(t, r, "receive_file", map[string]any{"path": "x.bin"}); err == nil {
		t.Fatal("size should be required")
	}
	if _, err := invoke(t, r, "receive_file", map[string]any{"path": "x.bin", "size": 1, "sha256": "abc"}); err == nil {
		t.Fatal("a malformed sha256 should be refused")
	}
	if _, err := invoke(t, r, "receive_file", map[string]any{"path": "../x.bin", "size": 1}); err == nil {
		t.Fatal("a path outside the workspace should be refused")
	}
	if _, err := invoke(t, r, "send_file", map[string]any{"path": "x.bin", "upload_url": "http://example.com/" + strings.Repeat("a", 64)}); ErrorCode(err) != "invalid_arguments" {
		t.Fatalf("send_file to another address: %v", err)
	}
}

func TestSendFileOnlyAcceptsQuickTunnelAddresses(t *testing.T) {
	token := strings.Repeat("ab", 32)
	for link, want := range map[string]bool{
		"https://calm-river-12.trycloudflare.com/" + token:        true,
		"http://calm-river-12.trycloudflare.com/" + token:         false,
		"https://calm-river-12.trycloudflare.com:8443/" + token:   false,
		"https://evil.example.com/" + token:                       false,
		"https://x.trycloudflare.com.evil.com/" + token:           false,
		"https://calm-river-12.trycloudflare.com/" + token + "?x": false,
		"https://calm-river-12.trycloudflare.com/short":           false,
		"https://user@calm-river-12.trycloudflare.com/" + token:   false,
	} {
		u, err := url.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		if got := quickTunnelURL(u); got != want {
			t.Errorf("%s: got %v", link, got)
		}
	}
}

// fakeCloud plays ReadyRig Cloud's /u/<device>/<token> route: it forwards each request
// to Relayed, splitting a PATCH body into frames that each name their offset.
func fakeCloud(t *testing.T, tr *Transfers, frame int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) != 4 || parts[1] != "u" {
			http.NotFound(w, r)
			return
		}
		token := parts[3]
		body, _ := io.ReadAll(r.Body)
		var status int
		var header http.Header
		var text []byte
		if r.Method != http.MethodPatch {
			status, header, text = tr.Relayed(context.Background(), r.Method, token, map[string]string{}, nil)
		} else {
			offset, _ := strconv.Atoi(r.Header.Get("Upload-Offset"))
			for sent := 0; ; {
				n := min(frame, len(body)-sent)
				h := map[string]string{"Upload-Offset": strconv.Itoa(offset + sent)}
				if sent+n == len(body) && r.Header.Get("Upload-SHA256") != "" {
					h["Upload-SHA256"] = r.Header.Get("Upload-SHA256")
				}
				status, header, text = tr.Relayed(context.Background(), http.MethodPatch, token, h, body[sent:sent+n])
				sent += n
				if status != http.StatusNoContent || sent == len(body) {
					break
				}
			}
		}
		for _, k := range []string{"Upload-Offset", "Upload-Length", "Upload-Complete", "Content-Type"} {
			if v := header.Get(k); v != "" {
				w.Header().Set(k, v)
			}
		}
		w.WriteHeader(status)
		w.Write(text)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/u/" + strings.Repeat("d", 43)
}

func TestRelayReceivingNeedsNoTunnel(t *testing.T) {
	r, tr, ft, root := transfersForTest(t)
	base := fakeCloud(t, tr, 48<<10)
	tr.RelayBase = func() string { return base }
	data := randomFile(t, root, "src.bin", 300<<10)
	out, err := invoke(t, r, "receive_file", map[string]any{"path": "in/dst.bin", "size": len(data)})
	if err != nil {
		t.Fatal(err)
	}
	v := asMap(t, out)
	link, _ := v["upload_url"].(string)
	if v["via"] != "relay" || !strings.HasPrefix(link, base+"/") {
		t.Fatalf("relay mode should be the default while it is connected: %v", v)
	}
	out, err = invoke(t, r, "send_file", map[string]any{"path": "src.bin", "upload_url": link, "yield_time_ms": 10000})
	if err != nil || asMap(t, out)["done"] != true {
		t.Fatalf("send over the relay: %v %v", out.Value, err)
	}
	r.WaitBackground()
	if got, _ := os.ReadFile(filepath.Join(root, "in", "dst.bin")); !bytes.Equal(got, data) {
		t.Fatal("file received over the relay differs")
	}
	if starts, _ := ft.counts(); starts != 0 {
		t.Fatalf("a tunnel was started %d times for a relay upload", starts)
	}
	eventually(t, "the janitor to stop once nothing is open", func() bool { tr.mu.Lock(); defer tr.mu.Unlock(); return tr.stop == nil })
}

func TestRelayLimitsAndChoices(t *testing.T) {
	r, tr, ft, root := transfersForTest(t)
	base := fakeCloud(t, tr, 1<<20)
	connected := true
	tr.RelayBase = func() string {
		if connected {
			return base
		}
		return ""
	}
	if _, err := invoke(t, r, "receive_file", map[string]any{"path": "big.bin", "size": maxRelayBytes + 1}); ErrorCode(err) != "too_large" {
		t.Fatalf("over 100 MB over the relay: %v", err)
	}
	tunneled := openUpload(t, r, map[string]any{"path": "t.bin", "size": 3, "via": "tunnel"})
	if starts, _ := ft.counts(); starts != 1 {
		t.Fatal("via=tunnel should start the tunnel")
	}
	// A tunnel address is not reachable through the relay route.
	token := tunneled[strings.LastIndex(tunneled, "/")+1:]
	if code, _ := upload(t, http.MethodHead, base+"/"+token, 0, nil); code != 404 {
		t.Fatalf("tunnel address over the relay: %d", code)
	}
	connected = false
	if _, err := invoke(t, r, "receive_file", map[string]any{"path": "r.bin", "size": 3, "via": "relay"}); ErrorCode(err) != "relay_unavailable" {
		t.Fatalf("relay while disconnected: %v", err)
	}
	// Without a relay the default is the tunnel.
	out, err := invoke(t, r, "receive_file", map[string]any{"path": "d.bin", "size": 3})
	if err != nil || asMap(t, out)["via"] != "tunnel" {
		t.Fatalf("default without a relay: %v %v", out.Value, err)
	}
	_ = root
}

func TestSendFileAcceptsRelayAddressesOfKnownClouds(t *testing.T) {
	tr := NewTransfers(nil)
	tr.RelayHosts = []string{"readyrig.getmegaportal.com"}
	device, token := strings.Repeat("A", 43), strings.Repeat("ab", 32)
	for link, want := range map[string]bool{
		"https://readyrig.getmegaportal.com/u/" + device + "/" + token:        true,
		"https://READYRIG.getmegaportal.com/u/" + device + "/" + token:        true,
		"http://readyrig.getmegaportal.com/u/" + device + "/" + token:         false,
		"https://evil.example.com/u/" + device + "/" + token:                  false,
		"https://readyrig.getmegaportal.com/u/short/" + token:                 false,
		"https://readyrig.getmegaportal.com/api/u/" + device + "/" + token:    false,
		"https://readyrig.getmegaportal.com/u/" + device + "/" + token + "?x": false,
	} {
		u, _ := url.Parse(link)
		if got := tr.allowURL(u); got != want {
			t.Errorf("%s: got %v", link, got)
		}
	}
}
