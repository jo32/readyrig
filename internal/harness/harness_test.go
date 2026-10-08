package harness

import (
	"computer-use-server/internal/store"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func registryForTest(t *testing.T) *Registry {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := New(s, "test-secret-token")
	t.Cleanup(func() { r.WaitBackground(); s.Close() })
	return r
}
func invoke(t *testing.T, r *Registry, name string, args any) (Output, error) {
	t.Helper()
	b, _ := json.Marshal(args)
	out, _, err := r.Invoke(context.Background(), name, Invocation{Session: "test-session", Arguments: b})
	return out, err
}
func TestFilesBoundaryAndRoundTrip(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("private"), 0600)
	os.Symlink(outside, filepath.Join(root, "escape"))
	f, err := NewFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := registryForTest(t)
	f.Register(r)
	for _, p := range []string{"../secret", "escape/secret", filepath.Join(outside, "secret")} {
		if _, err := invoke(t, r, "read_file", map[string]any{"path": p}); err == nil {
			t.Fatalf("read escaped: %s", p)
		}
		if _, err := invoke(t, r, "write_file", map[string]any{"path": p, "content": "overwrite"}); err == nil {
			t.Fatalf("write escaped: %s", p)
		}
	}
	if _, err := invoke(t, r, "write_file", map[string]any{"path": "notes/test.txt", "content": "one\ntwo\nthree"}); err != nil {
		t.Fatal(err)
	}
	out, err := invoke(t, r, "read_file", map[string]any{"path": "notes/test.txt", "start_line": 2, "end_line": 2})
	if err != nil || out.Value.(map[string]any)["content"] != "two" {
		t.Fatalf("line read: %v %v", out, err)
	}
	out, err = invoke(t, r, "search_files", map[string]any{"query": "two"})
	if err != nil || len(out.Value.(map[string]any)["matches"].([]map[string]any)) != 1 {
		t.Fatalf("search: %v %v", out, err)
	}
	b, _ := os.ReadFile(filepath.Join(outside, "secret"))
	if string(b) != "private" {
		t.Fatal("outside file changed")
	}
}
func TestPauseAndAuditRedaction(t *testing.T) {
	r := registryForTest(t)
	r.Register(Tool{Spec: Spec{Name: "check", Category: "files", Parallel: true, InputSchema: Schema(map[string]any{"text": Prop("string", "text")}, "text")}, Run: func(ctx context.Context, in Invocation) (Output, error) {
		return Output{Value: map[string]any{"token": "raw", "text": "test-secret-token"}}, nil
	}})
	_, err := invoke(t, r, "check", map[string]any{"text": "test-secret-token"})
	if err != nil {
		t.Fatal(err)
	}
	r.SetPaused(true)
	if _, err = invoke(t, r, "check", map[string]any{"text": "x"}); err == nil {
		t.Fatal("pause did not deny call")
	}
	rows, _, err := r.store.List(store.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Status != "denied" {
		t.Fatal(rows[0])
	}
	encoded, _ := json.Marshal(rows)
	if strings.Contains(string(encoded), "test-secret-token") || strings.Contains(string(encoded), `"raw"`) {
		t.Fatal("secret leaked to audit")
	}
}
func TestStrictArguments(t *testing.T) {
	r := registryForTest(t)
	f, _ := NewFiles(t.TempDir())
	defer f.Close()
	f.Register(r)
	for _, args := range []any{map[string]any{}, map[string]any{"path": 3}, map[string]any{"path": ".", "unexpected": true}, nil} {
		if _, err := invoke(t, r, "read_file", args); err == nil {
			t.Fatalf("accepted invalid args: %v", args)
		}
	}
}
func TestProcessesYieldStdinAndExit(t *testing.T) {
	r := registryForTest(t)
	p := NewProcesses(t.TempDir())
	defer p.Stop()
	p.Register(r)
	r.OnPause = p.Stop
	r.Enable("terminal", true)
	out, err := invoke(t, r, "exec_command", map[string]any{"command": "read value; printf 'reply:%s' \"$value\"", "yield_time_ms": 1})
	if err != nil {
		t.Fatal(err)
	}
	v := out.Value.(map[string]any)
	if v["running"] != true {
		t.Fatal(v)
	}
	out, err = invoke(t, r, "write_stdin", map[string]any{"session_id": v["session_id"], "chars": "hello\n", "yield_time_ms": 1000})
	if err != nil {
		t.Fatal(err)
	}
	v = out.Value.(map[string]any)
	if v["stdout"] != "reply:hello" || v["exit_code"] != 0 {
		t.Fatal(v)
	}
}
func TestTimeoutAndEmergencyStop(t *testing.T) {
	r := registryForTest(t)
	p := NewProcesses(t.TempDir())
	defer p.Stop()
	p.Register(r)
	r.OnPause = p.Stop
	r.Enable("terminal", true)
	out, err := invoke(t, r, "exec_command", map[string]any{"command": "sleep 30 & wait", "timeout": 1, "yield_time_ms": 2000})
	if err == nil || out.Value.(map[string]any)["timed_out"] != true {
		t.Fatalf("timeout failed: %v %v", out, err)
	}
	out, err = invoke(t, r, "exec_command", map[string]any{"command": "sleep 30 & wait", "yield_time_ms": 1})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Value.(map[string]any)["session_id"].(string)
	r.SetPaused(true)
	p.mu.Lock()
	pr := p.items[id]
	p.mu.Unlock()
	select {
	case <-pr.done:
	case <-time.After(3 * time.Second):
		t.Fatal("emergency stop did not cancel process group")
	}
}
func TestOutputKeepsHeadAndTailAndSpills(t *testing.T) {
	dir := t.TempDir()
	b := newStreamBuf(dir, "0123456789abcdef01234567.stdout")
	defer b.Close()
	b.Write([]byte("START-"))
	b.Write([]byte(strings.Repeat("x", 200*1024)))
	b.Write([]byte("-END"))
	text, cut := b.Drain(false)
	if !cut || len(text) > responseCap+100 || !strings.HasPrefix(text, "START-") || !strings.HasSuffix(text, "-END") || !strings.Contains(text, "bytes omitted") {
		t.Fatalf("head/tail broken: cut=%v len=%d", cut, len(text))
	}
	if b.path() != "spill:0123456789abcdef01234567.stdout" {
		t.Fatal("missing spill path", b.path())
	}
	b.Close()
	saved, err := os.ReadFile(filepath.Join(dir, "0123456789abcdef01234567.stdout"))
	if err != nil || len(saved) != 6+200*1024+4 || !strings.HasPrefix(string(saved), "START-") {
		t.Fatalf("spill incomplete: %d %v", len(saved), err)
	}
	if text, _ = b.Drain(false); text != "" {
		t.Fatal("drain repeated output")
	}
	snap, _ := b.Snapshot()
	if !strings.HasPrefix(snap, "START-") || !strings.HasSuffix(snap, "-END") {
		t.Fatal("snapshot lost an end")
	}
}
func TestSmallOutputIsNotSpilled(t *testing.T) {
	dir := t.TempDir()
	b := newStreamBuf(dir, "0123456789abcdef01234567.stdout")
	b.Write([]byte("hello"))
	if text, cut := b.Drain(false); text != "hello" || cut || b.path() != "" {
		t.Fatal(text, cut, b.path())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("small output created a file")
	}
}
func TestQueuedToolCancelledBeforeExecution(t *testing.T) {
	r := registryForTest(t)
	started := make(chan struct{}, 1)
	done := make(chan struct{}, 2)
	r.Register(Tool{Spec: Spec{Name: "slow", Category: "files", InputSchema: Schema(map[string]any{})}, Run: func(ctx context.Context, _ Invocation) (Output, error) {
		started <- struct{}{}
		<-ctx.Done()
		return Output{}, ctx.Err()
	}})
	go func() { invoke(t, r, "slow", map[string]any{}); done <- struct{}{} }()
	<-started
	go func() { invoke(t, r, "slow", map[string]any{}); done <- struct{}{} }()
	time.Sleep(20 * time.Millisecond)
	r.SetPaused(true)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("cancellation blocked")
		}
	}
	if len(started) > 0 {
		t.Fatal("queued tool executed after pause")
	}
}

func TestBackgroundExitUpdatesAuditWithoutPolling(t *testing.T) {
	r := registryForTest(t)
	p := NewProcesses(t.TempDir())
	defer p.Stop()
	p.Register(r)
	r.Enable("terminal", true)
	_, err := invoke(t, r, "exec_command", map[string]any{"command": "sleep 0.1; printf finished; exit 7", "yield_time_ms": 1})
	if err != nil {
		t.Fatal(err)
	}
	r.WaitBackground()
	rows, _, err := r.store.List(store.Filter{})
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	if rows[0].Status != "error" || !strings.Contains(string(rows[0].Result), "finished") {
		t.Fatalf("async audit not finalized: %+v", rows[0])
	}
}

func TestLiveAuditCancelAndCapabilityIsolation(t *testing.T) {
	r := registryForTest(t)
	p := NewProcesses(t.TempDir())
	defer p.Stop()
	p.Register(r)
	r.Enable("terminal", true)
	args := json.RawMessage(`{"command":"sleep 0.05; printf early; sleep 30","yield_time_ms":1}`)
	_, call, err := r.Invoke(context.Background(), "exec_command", Invocation{Session: "live", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	// Switching off an unrelated capability must not terminate the command.
	r.Enable("files", false)
	deadline := time.Now().Add(4 * time.Second)
	for {
		current, err := r.store.Get(call.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "running" {
			t.Fatalf("unrelated capability cancelled command: %+v", current)
		}
		if strings.Contains(string(current.Result), "early") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("live output was not persisted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !r.Cancel(call.ID) {
		t.Fatal("yielded call no longer cancellable")
	}
	r.WaitBackground()
	final, err := r.store.Get(call.ID)
	if err != nil || final.Status != "cancelled" || !strings.Contains(string(final.Result), "early") {
		t.Fatalf("bad cancelled record: %+v %v", final, err)
	}
	if r.Cancel(call.ID) {
		t.Fatal("finished call still active")
	}
}

func TestLiveSnapshotDoesNotConsumeAgentOutput(t *testing.T) {
	r := registryForTest(t)
	p := NewProcesses(t.TempDir())
	defer p.Stop()
	p.Register(r)
	r.Enable("terminal", true)
	out, err := invoke(t, r, "exec_command", map[string]any{"command": "sleep 0.1; printf retained; read value", "yield_time_ms": 1})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)
	result, err := invoke(t, r, "write_stdin", map[string]any{"session_id": out.Value.(map[string]any)["session_id"], "chars": "done\n"})
	if err != nil || result.Value.(map[string]any)["stdout"] != "retained" {
		t.Fatalf("console consumed agent output: %v %v", result, err)
	}
}
