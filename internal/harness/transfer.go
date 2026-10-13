package harness

import (
	"bytes"
	"computer-use-server/internal/tunnel"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MaxTransferBytes bounds one file moved with receive_file and send_file; a file
// received over the relay, whose bytes pass through ReadyRig Cloud, is held to
// maxRelayBytes.
const (
	MaxTransferBytes = 1 << 30
	maxRelayBytes    = 100 << 20
)

const (
	// Cloudflare refuses request bodies over 100 MB, so uploads go in pieces.
	defaultTransferChunk = 32 << 20
	maxIncoming          = 64
	partSuffix           = ".readyrig-part"
	stalePart            = 24 * time.Hour
	// A finished upload still answers HEAD for a while, for a sender that lost the last reply.
	finishedKept = 2 * time.Minute
)

var (
	transferToken = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sha256Hex     = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	quickHost     = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*\.trycloudflare\.com$`)
	relayPath     = regexp.MustCompile(`^/u/[A-Za-z0-9_-]{43}/[0-9a-f]{64}$`)
)

// PublicTunnel is the part of tunnel.Manager the receiver uses.
type PublicTunnel interface {
	Start(target string) error
	Stop() error
	Status() tunnel.Status
}

// Transfers moves files between computers without passing them through the agent.
// receive_file opens a one-file upload address on this computer and send_file
// uploads a local file to such an address. Every open address shares one local
// listener and one quick tunnel, separate from the sharing tunnel, which closes
// Grace after the last address closes.
type Transfers struct {
	files *Files
	// NewTunnel creates the receiver's tunnel. Without it receive_file is unavailable.
	NewTunnel func() PublicTunnel
	// Idle closes an address that gets no requests; Lifetime closes it regardless.
	Idle, Lifetime, Grace time.Duration
	// TunnelWait is how long receive_file waits for a new tunnel's address.
	TunnelWait time.Duration
	// Chunk is the most send_file puts in one request.
	Chunk int64
	// RelayBase is where uploads reach this computer over the relay
	// ("https://<cloud>/u/<device>"), or "" while the relay is not connected.
	RelayBase func() string
	// RelayHosts are the cloud hosts whose relay addresses send_file accepts.
	RelayHosts []string
	// AllowURL, when set, replaces the check of which addresses send_file may upload to.
	AllowURL func(*url.URL) bool
	Client   *http.Client

	mu      sync.Mutex
	byToken map[string]*incoming // open and recently finished uploads
	byPath  map[string]*incoming // open uploads by absolute destination
	srv     *http.Server
	local   string
	tun     PublicTunnel
	grace   *time.Timer
	stop    chan struct{}
	closed  bool
}

type incoming struct {
	token, path, abs, name, part, meta string
	dir                                *os.Root
	size                               int64
	sha                                string
	overwrite, relay                   bool
	created                            time.Time
	received, touched                  atomic.Int64
	// mu serialises writes to the staging file.
	mu sync.Mutex
	// Guarded by Transfers.mu.
	owned, ended, finished bool
	endedAt                time.Time
	done                   chan struct{}
	result                 map[string]any
	err                    error
}

func NewTransfers(files *Files) *Transfers {
	return &Transfers{files: files, Idle: 15 * time.Minute, Lifetime: 2 * time.Hour, Grace: 5 * time.Minute, TunnelWait: 40 * time.Second, Chunk: defaultTransferChunk,
		Client:  &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		byToken: map[string]*incoming{}, byPath: map[string]*incoming{}}
}

// allowURL accepts only the shapes of address receive_file hands out.
func (t *Transfers) allowURL(u *url.URL) bool {
	if t.AllowURL != nil {
		return t.AllowURL(u)
	}
	return quickTunnelURL(u) || relayURL(u, t.RelayHosts)
}

func plainHTTPS(u *url.URL) bool {
	return u.Scheme == "https" && u.User == nil && u.Port() == "" && u.RawQuery == "" && u.Fragment == ""
}

func quickTunnelURL(u *url.URL) bool {
	return plainHTTPS(u) && quickHost.MatchString(u.Host) && transferToken.MatchString(strings.TrimPrefix(u.Path, "/"))
}

func relayURL(u *url.URL, hosts []string) bool {
	if !plainHTTPS(u) || !relayPath.MatchString(u.Path) {
		return false
	}
	for _, h := range hosts {
		if h != "" && strings.EqualFold(u.Host, h) {
			return true
		}
	}
	return false
}

func (t *Transfers) Register(r *Registry) {
	r.Register(Tool{Spec: Spec{Name: "receive_file", Category: "files", Group: "advanced", Description: "Open a temporary upload address for one file so send_file on another computer, or any HTTP client, can deliver it here without the bytes passing through the agent. via=tunnel (up to 1 GiB) or relay (through ReadyRig Cloud, up to 100 MB; the default while relay mode is connected). Runs until the file arrives, its sha256 checks out and it is in place. Calling again with the same path and size keeps what arrived. Addresses close after 15 min idle or 2 h. HEAD gives Upload-Offset; PATCH sends from it.", Mutating: true, Parallel: true, InputSchema: Schema(map[string]any{"project": projectProp, "path": Prop("string", "Destination file path"), "size": limited("integer", "Exact file size in bytes (required unless cancel)", 0, MaxTransferBytes), "sha256": Prop("string", "Expected SHA-256 in hex (optional; send_file always sends one)"), "overwrite": Prop("boolean", "Replace an existing file"), "via": enum("How the sender reaches this computer", "tunnel", "relay"), "cancel": Prop("boolean", "Close this path's upload address and discard the partial file")}, "path")}, Run: t.files.tagged(t.receive)})
	r.Register(Tool{Spec: Spec{Name: "send_file", Category: "files", Group: "advanced", Description: "Upload a local file (up to 1 GiB) to an upload_url from receive_file on another computer. Continues from what the receiver already has and retries dropped connections. Waits yield_time_ms (default 5000), then continues in the background and notifies when done.", Mutating: true, Parallel: true, InputSchema: Schema(map[string]any{"project": projectProp, "path": Prop("string", "File to send"), "upload_url": Prop("string", "upload_url returned by receive_file"), "yield_time_ms": limited("integer", "How long to wait before continuing in the background, default 5000", 0, 30000)}, "path", "upload_url")}, Run: t.files.tagged(t.send)})
}

/* ================= receiving ================= */

func (t *Transfers) receive(ctx context.Context, in Invocation) (Output, error) {
	var a struct {
		Path      string `json:"path"`
		Project   string `json:"project"`
		Size      *int64 `json:"size"`
		SHA256    string `json:"sha256"`
		Overwrite bool   `json:"overwrite"`
		Via       string `json:"via"`
		Cancel    bool   `json:"cancel"`
	}
	if err := Decode(in.Arguments, &a); err != nil {
		return Output{}, err
	}
	root, rel, release, err := t.files.resolve(in.Session, a.Project, a.Path)
	if err != nil {
		return Output{}, err
	}
	abs := filepath.Join(root.Name(), rel)
	if a.Cancel {
		release()
		t.mu.Lock()
		e := t.byPath[abs]
		t.mu.Unlock()
		if e == nil {
			return Output{}, &ToolError{"not_found", "no upload address is open for this path"}
		}
		t.end(e, &ToolError{"cancelled", "receive_file was cancelled"}, true)
		return Output{Value: map[string]any{"path": a.Path, "resolved_path": abs, "cancelled": true}}, nil
	}
	fail := func(err error) (Output, error) { release(); return Output{}, err }
	switch {
	case a.Size == nil:
		return fail(errors.New("size is required"))
	case a.SHA256 != "" && !sha256Hex.MatchString(a.SHA256):
		return fail(errors.New("sha256 must be 64 hex characters"))
	}
	size, sha := *a.Size, strings.ToLower(a.SHA256)
	relayBase := ""
	if t.RelayBase != nil {
		relayBase = t.RelayBase()
	}
	via := a.Via
	if via == "" {
		// A computer in relay mode is one that should not run a tunnel.
		via = "tunnel"
		if relayBase != "" {
			via = "relay"
		}
	}
	switch {
	case via == "relay" && relayBase == "":
		return fail(&ToolError{"relay_unavailable", "relay mode is not connected on this computer"})
	case via == "relay" && size > maxRelayBytes:
		return fail(&ToolError{"too_large", "files over 100 MB cannot be received over the relay; use via=tunnel"})
	case via == "tunnel" && t.NewTunnel == nil:
		return fail(&ToolError{"unavailable", "receiving files over a tunnel is not available in this build"})
	}
	if err = root.MkdirAll(filepath.Dir(rel), 0755); err != nil {
		return fail(err)
	}
	mode := os.FileMode(0644)
	if info, err := root.Lstat(rel); err == nil {
		if info.IsDir() {
			return fail(&ToolError{"is_directory", "path is a directory"})
		}
		if !a.Overwrite {
			return fail(&ToolError{"file_exists", "file already exists (pass overwrite=true to replace it)"})
		}
		mode = info.Mode().Perm()
	}
	if size == 0 {
		err = writeAtomic(ctx, root, rel, nil, mode)
		release()
		if err != nil {
			return Output{}, err
		}
		return Output{Value: map[string]any{"path": a.Path, "resolved_path": abs, "size": 0, "done": true}}, nil
	}
	dir, err := root.OpenRoot(filepath.Dir(rel))
	release()
	if err != nil {
		return Output{}, err
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		dir.Close()
		return Output{}, errors.New("ReadyRig is shutting down")
	}
	if e := t.byPath[abs]; e != nil {
		if e.size == size && e.sha == sha && e.overwrite == a.Overwrite && e.relay == (via == "relay") {
			t.mu.Unlock()
			dir.Close()
			return t.opened(e)
		}
		// A different file for the same path replaces the old upload; prepare
		// then restarts its staging file, which no longer matches.
		t.mu.Unlock()
		t.end(e, &ToolError{"replaced", "a new receive_file call for this path replaced this upload"}, false)
		t.mu.Lock()
	}
	pending := size
	for _, o := range t.byPath {
		pending += o.size - o.received.Load()
	}
	if len(t.byPath) >= maxIncoming {
		t.mu.Unlock()
		dir.Close()
		return Output{}, &ToolError{"too_many_uploads", fmt.Sprintf("at most %d upload addresses can be open at once", maxIncoming)}
	}
	name := filepath.Base(rel)
	e := &incoming{token: newTransferToken(), path: a.Path, abs: abs, name: name, part: "." + name + partSuffix, dir: dir, size: size, sha: sha, overwrite: a.Overwrite, relay: via == "relay", created: time.Now(), done: make(chan struct{})}
	e.meta = e.part + ".json"
	received, err := prepare(e)
	if err == nil {
		if free := freeBytes(filepath.Dir(abs)); free >= 0 && pending-received > free {
			err = &ToolError{"insufficient_space", fmt.Sprintf("not enough free disk space: %d bytes are still to arrive, %d are free", pending-received, free)}
		}
	}
	if err != nil {
		t.mu.Unlock()
		dir.Close()
		return Output{}, err
	}
	e.received.Store(received)
	e.touched.Store(time.Now().UnixNano())
	t.byPath[abs], t.byToken[e.token] = e, e
	t.mu.Unlock()
	return t.opened(e)
}

// opened returns an upload's address. The first call that gets one owns the
// upload: it keeps running in the background until the file arrives.
func (t *Transfers) opened(e *incoming) (Output, error) {
	var base string
	var err error
	if e.relay {
		base, err = t.relayed()
	} else {
		base, err = t.ensure()
	}
	if err != nil {
		return Output{}, err
	}
	via := "tunnel"
	if e.relay {
		via = "relay"
	}
	value := map[string]any{"path": e.path, "resolved_path": e.abs, "upload_url": base + "/" + e.token, "via": via, "size": e.size, "received": e.received.Load(), "expires_in_s": int(t.Idle.Seconds())}
	t.mu.Lock()
	attach := !e.owned && !e.ended
	e.owned = e.owned || attach
	t.mu.Unlock()
	if !attach {
		return Output{Value: value}, nil
	}
	value["running"] = true
	return Output{
		Value:    value,
		Snapshot: func() any { return t.snapshot(e) },
		Completion: func() (any, error) {
			<-e.done
			t.mu.Lock()
			defer t.mu.Unlock()
			return e.result, e.err
		},
		// Pause, turning files off or a local cancel close the address and keep the partial file.
		Cancel: func() { t.end(e, context.Canceled, false) },
	}, nil
}

func (t *Transfers) snapshot(e *incoming) map[string]any {
	return map[string]any{"path": e.path, "resolved_path": e.abs, "size": e.size, "received": e.received.Load(), "running": true}
}

func newTransferToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

type partNote struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// prepare keeps a staging file left by an earlier upload of the same file, so
// the upload resumes, and starts an empty one otherwise.
func prepare(e *incoming) (int64, error) {
	cleanStale(e.dir, e.part, time.Now())
	want := partNote{e.size, e.sha}
	if b, err := e.dir.ReadFile(e.meta); err == nil {
		var have partNote
		if json.Unmarshal(b, &have) == nil && have == want {
			if info, err := e.dir.Lstat(e.part); err == nil && info.Mode().IsRegular() && info.Size() <= e.size {
				return info.Size(), nil
			}
		}
	}
	e.dir.Remove(e.part)
	b, _ := json.Marshal(want)
	if err := writeAtomic(context.Background(), e.dir, e.meta, b, 0600); err != nil {
		return 0, err
	}
	f, err := e.dir.OpenFile(e.part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	return 0, f.Close()
}

// cleanStale removes staging files nobody has touched for a day.
func cleanStale(dir *os.Root, keep string, now time.Time) {
	d, err := dir.Open(".")
	if err != nil {
		return
	}
	entries, _ := d.ReadDir(-1)
	d.Close()
	for _, entry := range entries {
		n := entry.Name()
		if n == keep || n == keep+".json" || !strings.HasPrefix(n, ".") || !strings.HasSuffix(n, partSuffix) && !strings.HasSuffix(n, partSuffix+".json") {
			continue
		}
		if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > stalePart {
			dir.Remove(n)
		}
	}
}

// ensure starts the shared listener and tunnel if needed and returns the public base address.
func (t *Transfers) ensure() (string, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return "", errors.New("ReadyRig is shutting down")
	}
	if t.grace != nil {
		t.grace.Stop()
		t.grace = nil
	}
	if t.srv == nil {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.mu.Unlock()
			return "", err
		}
		t.srv = &http.Server{Handler: http.HandlerFunc(t.serve), ReadHeaderTimeout: 30 * time.Second}
		t.local = "http://" + ln.Addr().String()
		go t.srv.Serve(ln)
	}
	t.startJanitor()
	if t.tun == nil {
		t.tun = t.NewTunnel()
	}
	tun, local := t.tun, t.local
	t.mu.Unlock()
	if st := tun.Status(); st.State == "stopped" || st.State == "error" {
		if err := tun.Start(local); err != nil {
			return "", &ToolError{"tunnel_failed", "could not start the receive tunnel: " + err.Error()}
		}
	}
	deadline := time.Now().Add(t.TunnelWait)
	for {
		st := tun.Status()
		switch {
		case st.State == "ready" && st.URL != "":
			return strings.TrimRight(st.URL, "/"), nil
		case st.State == "error":
			return "", &ToolError{"tunnel_failed", "the receive tunnel failed: " + st.Error}
		case time.Now().After(deadline):
			return "", &ToolError{"tunnel_starting", "the receive tunnel is still starting; call receive_file again in a few seconds (the upload is kept)"}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// relayed returns the relay base address; no listener or tunnel is needed.
func (t *Transfers) relayed() (string, error) {
	base := t.RelayBase()
	if base == "" {
		return "", &ToolError{"relay_unavailable", "relay mode is not connected on this computer"}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.grace != nil {
		t.grace.Stop()
		t.grace = nil
	}
	t.startJanitor()
	return strings.TrimRight(base, "/"), nil
}

// startJanitor expires idle addresses while any are open. Callers hold t.mu.
func (t *Transfers) startJanitor() {
	if t.stop == nil {
		t.stop = make(chan struct{})
		go t.janitor(t.stop)
	}
}

// end closes one upload address. discard also removes what arrived; it waits
// for a write in progress, so a caller that discards must not hold e.mu.
func (t *Transfers) end(e *incoming, err error, discard bool) {
	t.mu.Lock()
	if e.ended {
		t.mu.Unlock()
		return
	}
	e.ended, e.endedAt, e.err = true, time.Now(), err
	if t.byPath[e.abs] == e {
		delete(t.byPath, e.abs)
	}
	if !e.finished {
		delete(t.byToken, e.token)
	}
	close(e.done)
	t.armGrace()
	t.mu.Unlock()
	if discard {
		e.mu.Lock()
		e.dir.Remove(e.part)
		e.dir.Remove(e.meta)
		e.mu.Unlock()
	}
	go func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.dir.Close()
	}()
}

// armGrace schedules closing the tunnel once no address is open. Callers hold t.mu.
func (t *Transfers) armGrace() {
	if len(t.byPath) > 0 || t.srv == nil && t.stop == nil || t.grace != nil || t.closed {
		return
	}
	t.grace = time.AfterFunc(t.Grace, t.shutdownIfIdle)
}

func (t *Transfers) shutdownIfIdle() {
	t.mu.Lock()
	if len(t.byPath) > 0 {
		t.mu.Unlock()
		return
	}
	srv, tun, stop := t.detach()
	t.mu.Unlock()
	teardown(srv, tun, stop)
}

// detach takes the listener and tunnel out of service. Callers hold t.mu.
func (t *Transfers) detach() (*http.Server, PublicTunnel, chan struct{}) {
	srv, tun, stop := t.srv, t.tun, t.stop
	if t.grace != nil {
		t.grace.Stop()
	}
	t.srv, t.tun, t.stop, t.grace, t.local = nil, nil, nil, nil, ""
	t.byToken = map[string]*incoming{}
	return srv, tun, stop
}

func teardown(srv *http.Server, tun PublicTunnel, stop chan struct{}) {
	if stop != nil {
		close(stop)
	}
	if tun != nil {
		tun.Stop()
	}
	if srv != nil {
		srv.Close()
	}
}

// Close ends every upload, keeping partial files, and stops the tunnel.
func (t *Transfers) Close() {
	t.mu.Lock()
	t.closed = true
	open := make([]*incoming, 0, len(t.byPath))
	for _, e := range t.byPath {
		open = append(open, e)
	}
	t.mu.Unlock()
	for _, e := range open {
		t.end(e, context.Canceled, false)
	}
	t.mu.Lock()
	srv, tun, stop := t.detach()
	t.mu.Unlock()
	teardown(srv, tun, stop)
}

func (t *Transfers) janitor(stop chan struct{}) {
	every := max(min(t.Idle/4, 30*time.Second), 10*time.Millisecond)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		now := time.Now()
		var expired []*incoming
		t.mu.Lock()
		for _, e := range t.byPath {
			if now.Sub(time.Unix(0, e.touched.Load())) > t.Idle || now.Sub(e.created) > t.Lifetime {
				expired = append(expired, e)
			}
		}
		for token, e := range t.byToken {
			if e.finished && now.Sub(e.endedAt) > finishedKept {
				delete(t.byToken, token)
			}
		}
		t.mu.Unlock()
		for _, e := range expired {
			t.end(e, &ToolError{"expired", "the upload address expired; call receive_file again to resume"}, false)
		}
	}
}

/* ---- upload protocol ---- */

// Relayed answers one upload request that ReadyRig Cloud forwarded over the relay.
// It serves only addresses opened with via=relay.
func (t *Transfers) Relayed(ctx context.Context, method, token string, header map[string]string, body []byte) (int, http.Header, []byte) {
	t.mu.Lock()
	e := t.byToken[token]
	t.mu.Unlock()
	if e == nil || !e.relay {
		return http.StatusNotFound, http.Header{}, nil
	}
	req, err := http.NewRequestWithContext(ctx, method, "/"+token, bytes.NewReader(body))
	if err != nil {
		return http.StatusBadRequest, http.Header{}, nil
	}
	req.ContentLength = int64(len(body))
	for _, k := range []string{"Upload-Offset", "Upload-SHA256"} {
		if v, ok := header[k]; ok {
			req.Header.Set(k, v)
		}
	}
	rec := &recorder{header: http.Header{}}
	t.serve(rec, req)
	return rec.status(), rec.header, rec.body.Bytes()
}

// recorder captures a response that goes back over the relay instead of a connection.
type recorder struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.body.Write(b)
}
func (r *recorder) status() int {
	if r.code == 0 {
		return http.StatusOK
	}
	return r.code
}

func (t *Transfers) serve(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/")
	t.mu.Lock()
	e := t.byToken[token]
	t.mu.Unlock()
	if e == nil || !transferToken.MatchString(token) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodHead, http.MethodGet:
		t.mu.Lock()
		finished := e.finished
		t.mu.Unlock()
		received := e.received.Load()
		setOffsets(w, e, received, finished)
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"name": e.name, "size": e.size, "received": received, "done": finished})
		}
	case http.MethodPatch:
		t.patch(w, r, e)
	case http.MethodDelete:
		t.end(e, &ToolError{"cancelled", "the sender cancelled the upload"}, true)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "HEAD, GET, PATCH, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func setOffsets(w http.ResponseWriter, e *incoming, received int64, finished bool) {
	h := w.Header()
	h.Set("Upload-Offset", strconv.FormatInt(received, 10))
	h.Set("Upload-Length", strconv.FormatInt(e.size, 10))
	if finished {
		h.Set("Upload-Complete", "1")
	}
}

// patch writes one piece. Its Upload-Offset may repeat bytes that already
// arrived (they are replaced), so a sender can always resend safely.
func (t *Transfers) patch(w http.ResponseWriter, r *http.Request, e *incoming) {
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset < 0 {
		http.Error(w, "Upload-Offset header required", http.StatusBadRequest)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	t.mu.Lock()
	ended, finished := e.ended, e.finished
	t.mu.Unlock()
	switch {
	case finished:
		setOffsets(w, e, e.size, true)
		if offset == e.size {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusConflict)
		}
		return
	case ended:
		http.NotFound(w, r)
		return
	}
	received := e.received.Load()
	if offset > received {
		setOffsets(w, e, received, false)
		w.WriteHeader(http.StatusConflict)
		return
	}
	limit := e.size - offset
	if r.ContentLength > limit {
		http.Error(w, "the body runs past the declared size", http.StatusRequestEntityTooLarge)
		return
	}
	f, err := e.dir.OpenFile(e.part, os.O_WRONLY, 0)
	if err != nil {
		http.Error(w, "the staging file is unavailable", http.StatusInternalServerError)
		return
	}
	if offset < received {
		err = f.Truncate(offset)
		e.received.Store(offset)
	}
	if err == nil {
		_, err = f.Seek(offset, io.SeekStart)
	}
	var n int64
	if err == nil {
		n, err = io.Copy(&staging{f: f, e: e, at: offset}, io.LimitReader(r.Body, limit+1))
	}
	if n > limit {
		f.Truncate(offset)
		e.received.Store(offset)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	e.touched.Store(time.Now().UnixNano())
	switch {
	case n > limit:
		http.Error(w, "the body runs past the declared size", http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		// The sender went away mid-piece; what arrived is kept and reported.
		setOffsets(w, e, e.received.Load(), false)
		http.Error(w, "the upload was interrupted", http.StatusBadRequest)
		return
	case e.received.Load() < e.size:
		setOffsets(w, e, e.received.Load(), false)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	result, err := finish(e, r.Header.Get("Upload-SHA256"))
	if err != nil {
		// finish already discarded a file that failed its check.
		t.end(e, err, false)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	t.mu.Lock()
	if !e.ended {
		e.finished, e.result = true, result
	}
	t.mu.Unlock()
	t.end(e, nil, false)
	setOffsets(w, e, e.size, true)
	w.WriteHeader(http.StatusNoContent)
}

// staging records progress as bytes reach the staging file.
type staging struct {
	f  *os.File
	e  *incoming
	at int64
}

func (s *staging) Write(b []byte) (int, error) {
	n, err := s.f.Write(b)
	s.at += int64(n)
	s.e.received.Store(s.at)
	s.e.touched.Store(time.Now().UnixNano())
	return n, err
}

// finish checks the complete staging file and renames it into place. Callers hold e.mu.
func finish(e *incoming, claimed string) (map[string]any, error) {
	f, err := e.dir.Open(e.part)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		return nil, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	for _, want := range []string{e.sha, strings.ToLower(claimed)} {
		if want != "" && want != sum {
			e.dir.Remove(e.part)
			e.dir.Remove(e.meta)
			e.received.Store(0)
			return nil, &ToolError{"hash_mismatch", fmt.Sprintf("the received file has sha256 %s, expected %s; it was discarded", sum, want)}
		}
	}
	mode := os.FileMode(0644)
	if info, err := e.dir.Lstat(e.name); err == nil {
		if info.IsDir() {
			return nil, &ToolError{"is_directory", "path is a directory"}
		}
		if !e.overwrite {
			return nil, &ToolError{"file_exists", "the file appeared while it was being received (overwrite is false)"}
		}
		mode = info.Mode().Perm()
	}
	if err = e.dir.Chmod(e.part, mode); err != nil {
		return nil, err
	}
	if err = e.dir.Rename(e.part, e.name); err != nil {
		return nil, err
	}
	e.dir.Remove(e.meta)
	return map[string]any{"path": e.path, "resolved_path": e.abs, "size": e.size, "sha256": sum, "done": true}, nil
}
