package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestUnknownArgumentIsReportedBeforeMissingOne(t *testing.T) {
	r, _ := execForTest(t)
	_, err := invoke(t, r, "exec_command", map[string]any{"cmd": "true"})
	if err == nil || err.Error() != "unknown argument: cmd (did you mean command?)" {
		t.Fatalf("got %v", err)
	}
	_, err = invoke(t, r, "exec_command", map[string]any{"command": "true", "zzzzzz": 1})
	if err == nil || !strings.Contains(err.Error(), "(valid: ") || !strings.Contains(err.Error(), "command") {
		t.Fatalf("no close match should list the valid names: %v", err)
	}
}

func TestKilledCommandNamesTheSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX signals")
	}
	r, _ := execForTest(t)
	out, err := invoke(t, r, "exec_command", map[string]any{"command": "kill -9 $$", "yield_time_ms": 5000})
	if err != nil {
		t.Fatal(err)
	}
	v := asMap(t, out)
	if v["exit_code"] != -1 || v["signal"] != "killed" || out.Failure != "command was killed by signal: killed" {
		t.Fatalf("failure=%q value=%v", out.Failure, v)
	}
}

func TestSessionNotFoundNamesTheSession(t *testing.T) {
	r, _ := execForTest(t)
	_, err := invoke(t, r, "write_stdin", map[string]any{"session_id": "PLACEHOLDER"})
	if ErrorCode(err) != "session_not_found" || !strings.Contains(err.Error(), `"PLACEHOLDER"`) || !strings.Contains(err.Error(), "list_tasks") {
		t.Fatalf("code %q err %v", ErrorCode(err), err)
	}
}

func TestClientCancelIsDescribed(t *testing.T) {
	r, _ := execForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	b, _ := json.Marshal(map[string]any{"command": "sleep 5", "yield_time_ms": 5000})
	_, call, err := r.Invoke(ctx, "exec_command", Invocation{Session: "s", Arguments: b})
	if !errors.Is(err, context.Canceled) || ErrorCode(err) != "cancelled" {
		t.Fatalf("cancellation must stay detectable: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "cancelled after ") || !strings.Contains(err.Error(), "client disconnected") {
		t.Fatalf("got %q", err)
	}
	saved, _ := r.store.Get(call.ID)
	if saved.Status != "cancelled" || saved.Error != err.Error() {
		t.Fatalf("audit: %+v", saved)
	}
}

func TestPathErrorsDropTheSyscall(t *testing.T) {
	err := describe(&fs.PathError{Op: "openat", Path: "build/i0.jpg", Err: fs.ErrNotExist}, false, 0)
	if err.Error() != "no such file or directory: build/i0.jpg" || ErrorCode(err) != "not_found" {
		t.Fatalf("%q %q", err, ErrorCode(err))
	}
	wrapped := fmt.Errorf("read config: %w", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist})
	if describe(wrapped, false, 0) != wrapped {
		t.Fatal("wrapped path errors keep their own context")
	}
}
