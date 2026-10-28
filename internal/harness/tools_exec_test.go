package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"computer-use-server/internal/store"
)

func execForTest(t *testing.T) (*Registry, *Processes) {
	t.Helper()
	r := registryForTest(t)
	p := NewProcesses(t.TempDir())
	p.SpillDir = filepath.Join(t.TempDir(), "spill")
	t.Cleanup(p.Stop)
	p.Register(r)
	r.Enable("terminal", true)
	return r, p
}
func TestNonZeroExitIsDataNotToolError(t *testing.T) {
	r, _ := execForTest(t)
	b, _ := json.Marshal(map[string]any{"command": "printf out; printf err >&2; exit 3"})
	out, call, err := r.Invoke(context.Background(), "exec_command", Invocation{Session: "s", Arguments: b})
	if err != nil {
		t.Fatalf("non-zero exit became a tool error: %v", err)
	}
	v := asMap(t, out)
	if v["exit_code"] != 3 || v["stdout"] != "out" || v["stderr"] != "err" || v["running"] != false {
		t.Fatal(v)
	}
	if out.Failure != "command exited with code 3" || !strings.Contains(out.Text, "out\n[stderr]\nerr\n[exit code 3]") {
		t.Fatalf("failure=%q text=%q", out.Failure, out.Text)
	}
	saved, err := r.store.Get(call.ID)
	if err != nil || saved.Status != "error" || saved.Error != "command exited with code 3" {
		t.Fatalf("audit must still flag the failure: %+v %v", saved, err)
	}
	out, err = invoke(t, r, "exec_command", map[string]any{"command": "printf fine"})
	if err != nil || out.Failure != "" || out.Text != "fine\n[exit code 0]\n" {
		t.Fatalf("success run: %q %v", out.Text, err)
	}
}
func TestTimeoutIsAStructuredToolError(t *testing.T) {
	r, _ := execForTest(t)
	_, err := invoke(t, r, "exec_command", map[string]any{"command": "sleep 30 & wait", "timeout": 1, "yield_time_ms": 3000})
	if ErrorCode(err) != "timeout" {
		t.Fatalf("code %q err %v", ErrorCode(err), err)
	}
}
func TestLongOutputIsCutAndReadableFromSpill(t *testing.T) {
	r, p := execForTest(t)
	f, err := NewFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.SpillDir = p.SpillDir
	f.Register(r)
	out, err := invoke(t, r, "exec_command", map[string]any{"command": "echo FIRST; i=0; while [ $i -lt 4000 ]; do echo \"filler line number $i\"; i=$((i+1)); done; echo LAST", "yield_time_ms": 3000})
	if err != nil {
		t.Fatal(err)
	}
	v := asMap(t, out)
	stdout := v["stdout"].(string)
	if v["truncated"] != true || len(stdout) > responseCap+100 || !strings.HasPrefix(stdout, "FIRST\n") || !strings.HasSuffix(stdout, "LAST\n") || !strings.Contains(stdout, "bytes omitted") {
		t.Fatalf("head/tail: truncated=%v len=%d", v["truncated"], len(stdout))
	}
	path, _ := v["stdout_path"].(string)
	if !strings.HasPrefix(path, "spill:") || !strings.Contains(out.Text, path) {
		t.Fatalf("spill path missing: %v", v)
	}
	read, err := invoke(t, r, "read_file", map[string]any{"path": path, "start_line": 2000, "limit": 2})
	if err != nil || asMap(t, read)["content"] != "filler line number 1998\nfiller line number 1999" || asMap(t, read)["total_lines"] != 4002 {
		t.Fatalf("spill content: %v %v", read.Value, err)
	}
	old := filepath.Join(p.SpillDir, "aaaaaaaaaaaaaaaaaaaaaaaa.stdout")
	os.WriteFile(old, []byte("stale"), 0600)
	stamp := time.Now().Add(-48 * time.Hour)
	os.Chtimes(old, stamp, stamp)
	invoke(t, r, "exec_command", map[string]any{"command": "true"})
	if _, err := os.Stat(old); err == nil {
		t.Fatal("old spill file kept")
	}
}
func TestBackgroundCommandNotifiesWhenDone(t *testing.T) {
	r, _ := execForTest(t)
	events, cancel := r.Subscribe("test-session")
	defer cancel()
	started := time.Now()
	out, err := invoke(t, r, "exec_command", map[string]any{"command": "sleep 1; echo done; exit 4", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	if asMap(t, out)["running"] != true || time.Since(started) > 700*time.Millisecond {
		t.Fatalf("background call blocked for %v: %v", time.Since(started), out.Value)
	}
	select {
	case e := <-events:
		if e.Kind != "task_finished" || e.Data["exit_code"] != 4 || e.Data["session_id"] != asMap(t, out)["session_id"] || e.Data["tool"] != "exec_command" {
			t.Fatalf("event: %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no task_finished event")
	}
	other, cancelOther := r.Subscribe("someone-else")
	defer cancelOther()
	select {
	case e := <-other:
		t.Fatalf("event leaked to another session: %+v", e)
	default:
	}
}
func TestCommandEnvironment(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("SEC_TOKEN_PATH", "/etc/identity.token")
	t.Setenv("READYRIG_UPDATE_TOKEN", "hunter2")
	t.Setenv("RELAY_UPDATE_TOKEN", "hunter3")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("SHELL", "/bin/zsh")
	r, p := execForTest(t)
	file := filepath.Join(t.TempDir(), "command-env.json")
	env, err := NewCommandEnv(file)
	if err != nil {
		t.Fatal(err)
	}
	p.Env = env
	echo := func() []string {
		t.Helper()
		out, err := invoke(t, r, "exec_command", map[string]any{"command": "echo \"$PATH|$SEC_TOKEN_PATH|$READYRIG_UPDATE_TOKEN$RELAY_UPDATE_TOKEN|$TERM|$SHELL\""})
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(strings.TrimSpace(asMap(t, out)["stdout"].(string)), "|")
		if len(parts) != 5 {
			t.Fatalf("env: %q", parts)
		}
		return parts
	}
	// By default commands inherit the environment, without ReadyRig's own settings.
	parts := echo()
	if parts[1] != "/etc/identity.token" || parts[2] != "" || parts[3] != "dumb" || parts[4] != "/bin/zsh" {
		t.Fatalf("inherited env: %q", parts)
	}
	if _, err := os.Stat("/opt/homebrew/bin"); err == nil && !strings.Contains(parts[0], "/opt/homebrew/bin") {
		t.Fatalf("homebrew missing from PATH: %s", parts[0])
	}
	// Turned off, they get only the short list, and the choice is saved.
	if err := env.SetInherit(false); err != nil {
		t.Fatal(err)
	}
	if parts := echo(); parts[1] != "" || parts[2] != "" || parts[3] != "dumb" || parts[4] != "/bin/zsh" {
		t.Fatalf("clean env: %q", parts)
	}
	if reopened, err := NewCommandEnv(file); err != nil || reopened.Inherit() {
		t.Fatalf("setting not saved: %v", err)
	}
	out, err := invoke(t, r, "exec_command", map[string]any{"command": "echo $0 $-", "login_shell": true})
	if err != nil || !strings.Contains(asMap(t, out)["stdout"].(string), "sh") {
		t.Fatalf("login shell: %v %v", out.Value, err)
	}
}
func TestLongTimeoutAllowedAndValidated(t *testing.T) {
	r, _ := execForTest(t)
	for _, bad := range []int{0 - 1, 14401} {
		if _, err := invoke(t, r, "exec_command", map[string]any{"command": "true", "timeout": bad}); err == nil {
			t.Fatalf("timeout %d accepted", bad)
		}
	}
	if _, err := invoke(t, r, "exec_command", map[string]any{"command": "true", "timeout": 3600}); err != nil {
		t.Fatal(err)
	}
}
func TestPerCategoryLocksDoNotBlockEachOther(t *testing.T) {
	r := registryForTest(t)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	r.Register(Tool{Spec: Spec{Name: "slow_browser", Category: "browser", InputSchema: Schema(map[string]any{})}, Run: func(ctx context.Context, _ Invocation) (Output, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return Output{Value: map[string]any{}}, nil
	}})
	r.Register(Tool{Spec: Spec{Name: "quick_file", Category: "files", InputSchema: Schema(map[string]any{})}, Run: func(context.Context, Invocation) (Output, error) {
		return Output{Value: map[string]any{"ok": true}}, nil
	}})
	r.Register(Tool{Spec: Spec{Name: "other_browser", Category: "browser", InputSchema: Schema(map[string]any{})}, Run: func(context.Context, Invocation) (Output, error) {
		return Output{Value: map[string]any{"ok": true}}, nil
	}})
	done := make(chan struct{})
	go func() { invoke(t, r, "slow_browser", map[string]any{}); close(done) }()
	<-started
	fileDone := make(chan error, 1)
	go func() { _, err := invoke(t, r, "quick_file", map[string]any{}); fileDone <- err }()
	select {
	case err := <-fileDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("a browser call blocked a file tool")
	}
	sameDone := make(chan struct{})
	go func() { invoke(t, r, "other_browser", map[string]any{}); close(sameDone) }()
	select {
	case <-sameDone:
		t.Fatal("browser calls must still run one at a time")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for _, c := range []chan struct{}{done, sameDone} {
		select {
		case <-c:
		case <-time.After(2 * time.Second):
			t.Fatal("browser queue stuck")
		}
	}
}
func TestDescriptionIsAuditedButNotPassedToHandler(t *testing.T) {
	r := registryForTest(t)
	var seen string
	r.Register(Tool{Spec: Spec{Name: "mutate", Category: "files", Mutating: true, Parallel: true, InputSchema: Schema(map[string]any{"x": Prop("string", "x")}, "x")}, Run: func(_ context.Context, in Invocation) (Output, error) {
		seen = string(in.Arguments)
		var a struct{ X string }
		if err := Decode(in.Arguments, &a); err != nil {
			return Output{}, err
		}
		return Output{Value: map[string]any{"x": a.X}}, nil
	}})
	r.Register(Tool{Spec: Spec{Name: "reader", Category: "files", Parallel: true, InputSchema: Schema(map[string]any{})}, Run: func(context.Context, Invocation) (Output, error) { return Output{}, nil }})
	_, call, err := r.Invoke(context.Background(), "mutate", Invocation{Session: "s", Arguments: json.RawMessage(`{"x":"1","description":"set x for the report"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(seen, "description") {
		t.Fatalf("handler saw description: %s", seen)
	}
	saved, _ := r.store.Get(call.ID)
	if !strings.Contains(string(saved.Arguments), "set x for the report") {
		t.Fatalf("intent missing from audit: %s", saved.Arguments)
	}
	for _, s := range r.Specs() {
		_, has := s.InputSchema["properties"].(map[string]any)["description"]
		if has != (s.Name == "mutate") {
			t.Fatalf("%s: description property = %v", s.Name, has)
		}
	}
	if _, err = invoke(t, r, "reader", map[string]any{"description": "nope"}); err == nil {
		t.Fatal("read-only tool accepted a description")
	}
}
func TestStructuredErrorsAndSchemaConstraints(t *testing.T) {
	r := registryForTest(t)
	r.Register(Tool{Spec: Spec{Name: "typed", Category: "files", Parallel: true, InputSchema: Schema(map[string]any{"mode": enum("mode", "a", "b"), "n": limited("integer", "n", 1, 5), "pair": map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "minItems": 2, "maxItems": 2}})}, Run: func(context.Context, Invocation) (Output, error) {
		return Output{Value: map[string]any{}}, nil
	}})
	for _, bad := range []map[string]any{{"mode": "c"}, {"n": 0}, {"n": 6}, {"pair": []any{1}}, {"pair": []any{1, 2, 3}}, {"pair": []any{1, "x"}}} {
		_, err := invoke(t, r, "typed", bad)
		if ErrorCode(err) != "invalid_arguments" {
			t.Fatalf("%v accepted or wrong code %q", bad, ErrorCode(err))
		}
	}
	if _, err := invoke(t, r, "typed", map[string]any{"mode": "a", "n": 5, "pair": []any{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if code := ErrorCode(func() error { _, err := invoke(t, r, "nope", map[string]any{}); return err }()); code != "unknown_tool" {
		t.Fatal(code)
	}
	r.Enable("files", false)
	_, err := invoke(t, r, "typed", map[string]any{})
	if ErrorCode(err) != "capability_disabled" || !strings.Contains(err.Error(), "files") {
		t.Fatalf("%q %v", ErrorCode(err), err)
	}
	r.Enable("files", true)
	r.SetPaused(true)
	if _, err = invoke(t, r, "typed", map[string]any{}); ErrorCode(err) != "control_paused" {
		t.Fatalf("%q", ErrorCode(err))
	}
	if ErrorCode(context.Canceled) != "cancelled" || ErrorCode(nil) != "" || ErrorCode(os.ErrNotExist) != "not_found" || ErrorCode(os.ErrPermission) != "permission_denied" || ErrorCode(os.ErrExist) != "file_exists" || ErrorCode(errors.New("boom")) != "tool_error" {
		t.Fatal("ErrorCode classification")
	}
}
func TestToolListSignalsAndFilters(t *testing.T) {
	r := registryForTest(t)
	r.Register(Tool{Spec: Spec{Name: "f", Category: "files", InputSchema: Schema(map[string]any{})}})
	r.Register(Tool{Spec: Spec{Name: "x", Category: "terminal", InputSchema: Schema(map[string]any{})}})
	if specs := r.ListedSpecs(); len(specs) != 1 || specs[0].Name != "f" {
		t.Fatalf("disabled capability advertised: %+v", specs)
	}
	changed := r.ToolListChanged()
	r.Enable("terminal", true)
	select {
	case <-changed:
	default:
		t.Fatal("enabling a capability did not signal a tool-list change")
	}
	if len(r.ListedSpecs()) != 2 {
		t.Fatal("enabled capability not advertised")
	}
	changed = r.ToolListChanged()
	r.Enable("terminal", true)
	select {
	case <-changed:
		t.Fatal("no-op toggle signalled")
	default:
	}
	r.ReplaceCategory("browser", []Tool{{Spec: Spec{Name: "chrome_a", Category: "browser", InputSchema: map[string]any{"type": "object"}}, External: true}})
	select {
	case <-changed:
	default:
		t.Fatal("browser tools appearing did not signal")
	}
}
func TestRedactionKeepsCountersAndHidesCredentials(t *testing.T) {
	for key, want := range map[string]bool{"token": true, "access_token": true, "refreshToken": true, "password": true, "client_secret": true, "Authorization": true, "api_key": true, "max_tokens": false, "tokens": false, "input_tokens": false, "tokenizer": false, "name": false} {
		if got := sensitiveKey(strings.ToLower(key)); got != want {
			t.Errorf("sensitiveKey(%q) = %v, want %v", key, got, want)
		}
	}
}
func TestAuditStatusOfFinishedBackgroundCallIsError(t *testing.T) {
	r, _ := execForTest(t)
	_, call, err := r.Invoke(context.Background(), "exec_command", Invocation{Session: "s", Arguments: json.RawMessage(`{"command":"sleep 0.3; exit 2","background":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	r.WaitBackground()
	saved, err := r.store.Get(call.ID)
	if err != nil || saved.Status != "error" || saved.Error != "command exited with code 2" {
		t.Fatalf("%+v %v", saved, err)
	}
	var _ store.Call = saved
}

func TestFinishedBackgroundJobIsHeldForTheNextCall(t *testing.T) {
	r, _ := execForTest(t)
	out, err := invoke(t, r, "exec_command", map[string]any{"command": "sleep 0.3; echo hi; exit 6", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	r.WaitBackground()
	notices := r.TakeNotices("test-session")
	if len(notices) != 1 || notices[0].Kind != "task_finished" || notices[0].Data["exit_code"] != 6 || notices[0].Data["session_id"] != asMap(t, out)["session_id"] {
		t.Fatalf("held notices: %+v", notices)
	}
	if again := r.TakeNotices("test-session"); len(again) != 0 {
		t.Fatal("notices delivered twice")
	}
	if other := r.TakeNotices("someone-else"); len(other) != 0 {
		t.Fatal("notices leaked across sessions")
	}
	events, cancel := r.Subscribe("test-session")
	defer cancel()
	r.Publish(Event{Session: "test-session", Kind: "task_finished", Data: map[string]any{}})
	select {
	case <-events:
	default:
		t.Fatal("live subscriber missed the event")
	}
	if held := r.TakeNotices("test-session"); len(held) != 0 {
		t.Fatal("an event delivered to a live stream was also held")
	}
}
func TestHeldNoticesAreBounded(t *testing.T) {
	r := registryForTest(t)
	for i := 0; i < 100; i++ {
		r.Publish(Event{Session: "s", Kind: "task_finished", Data: map[string]any{"n": i}})
	}
	q := r.TakeNotices("s")
	if len(q) != 32 || q[31].Data["n"] != 99 || q[0].Data["n"] != 68 {
		t.Fatalf("%d held, first %v", len(q), q[0].Data)
	}
	for i := 0; i < 600; i++ {
		r.Publish(Event{Session: fmt.Sprint("session-", i), Kind: "x"})
	}
	r.mu.Lock()
	n := len(r.pending)
	r.mu.Unlock()
	if n > 512 {
		t.Fatalf("%d sessions held", n)
	}
}

func TestTerminateIsTheRequestedOutcomeNotAnError(t *testing.T) {
	r, _ := execForTest(t)
	events, cancel := r.Subscribe("test-session")
	defer cancel()
	out, call, err := r.Invoke(context.Background(), "exec_command", Invocation{Session: "test-session", Arguments: []byte(`{"command":"printf partial; sleep 30","yield_time_ms":300}`)})
	if err != nil || asMap(t, out)["running"] != true {
		t.Fatal(out.Value, err)
	}
	id := asMap(t, out)["session_id"]
	res, err := invoke(t, r, "write_stdin", map[string]any{"session_id": id, "terminate": true})
	if err != nil {
		t.Fatalf("a requested terminate must not be a tool error: %v", err)
	}
	v := asMap(t, res)
	if v["running"] != false || v["terminated"] != true || v["stdout"] != "" {
		t.Fatalf("result: %v", v)
	}
	if _, has := v["exit_code"]; has {
		t.Fatal("a killed process has no exit code to report")
	}
	if res.Failure != "" || !strings.Contains(res.Text, "[terminated at your request]") || strings.Contains(res.Text, "exit code") {
		t.Fatalf("failure=%q text=%q", res.Failure, res.Text)
	}
	r.WaitBackground()
	saved, err := r.store.Get(call.ID)
	if err != nil || saved.Status != "success" || saved.Error != "" {
		t.Fatalf("the original call must not be marked failed: %+v %v", saved, err)
	}
	select {
	case e := <-events:
		if e.Data["terminated"] != true || e.Data["status"] != "success" {
			t.Fatalf("event: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no task_finished event")
	}
	tasks, err := invoke(t, r, "list_tasks", map[string]any{})
	if err != nil || !strings.Contains(tasks.Text, "terminated after") || strings.Contains(tasks.Text, "exited") {
		t.Fatalf("%q %v", tasks.Text, err)
	}
	// Terminating a process that already exited leaves its real status alone.
	done, _ := invoke(t, r, "exec_command", map[string]any{"command": "sleep 0.3; exit 5", "yield_time_ms": 1})
	time.Sleep(600 * time.Millisecond)
	late, err := invoke(t, r, "write_stdin", map[string]any{"session_id": asMap(t, done)["session_id"], "terminate": true})
	if lv := asMap(t, late); err != nil || lv["exit_code"] != 5 || lv["terminated"] != nil {
		t.Fatalf("late terminate rewrote the outcome: %v %v", lv, err)
	}
}
func TestPauseStillCancelsWithAnError(t *testing.T) {
	r, p := execForTest(t)
	r.OnPause = p.Stop
	out, call, err := r.Invoke(context.Background(), "exec_command", Invocation{Session: "s", Arguments: []byte(`{"command":"sleep 30","yield_time_ms":1}`)})
	if err != nil || asMap(t, out)["running"] != true {
		t.Fatal(out.Value, err)
	}
	r.SetPaused(true)
	r.WaitBackground()
	saved, _ := r.store.Get(call.ID)
	if saved.Status != "cancelled" {
		t.Fatalf("a pause is not the agent's choice and must stay a cancellation: %+v", saved)
	}
}
