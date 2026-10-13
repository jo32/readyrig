package harness

import (
	"bytes"
	"computer-use-server/internal/tunnel"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTransferOverRealQuickTunnel sends a file through a real trycloudflare.com
// tunnel. It needs cloudflared and the internet, so it only runs with
// READYRIG_LIVE_TUNNEL=1 (optionally READYRIG_CLOUDFLARED=/path/to/cloudflared).
func TestTransferOverRealQuickTunnel(t *testing.T) {
	if os.Getenv("READYRIG_LIVE_TUNNEL") != "1" {
		t.Skip("set READYRIG_LIVE_TUNNEL=1 to run against a real quick tunnel")
	}
	r, f, root := filesForTest(t)
	tr := NewTransfers(f)
	dir := t.TempDir()
	tr.NewTunnel = func() PublicTunnel {
		return tunnel.New(tunnel.Options{Dir: dir, Command: os.Getenv("READYRIG_CLOUDFLARED")})
	}
	tr.TunnelWait = 90 * time.Second
	tr.Chunk = 8 << 20
	tr.Grace = time.Second
	tr.Register(r)
	t.Cleanup(tr.Close)
	data := randomFile(t, root, "src.bin", 20<<20)
	started := time.Now()
	link := openUpload(t, r, map[string]any{"path": "in/dst.bin", "size": len(data)})
	t.Logf("tunnel ready after %s: %s", time.Since(started).Round(time.Millisecond), link[:40]+"…")
	started = time.Now()
	out, err := invoke(t, r, "send_file", map[string]any{"path": "src.bin", "upload_url": link, "yield_time_ms": 30000})
	if err != nil {
		t.Fatal(err)
	}
	if asMap(t, out)["done"] != true {
		t.Fatalf("still running after 30 s: %v", out.Value)
	}
	t.Logf("sent 20 MiB in %s", time.Since(started).Round(time.Millisecond))
	r.WaitBackground()
	if got, _ := os.ReadFile(filepath.Join(root, "in", "dst.bin")); !bytes.Equal(got, data) {
		t.Fatal("delivered file differs")
	}
}
