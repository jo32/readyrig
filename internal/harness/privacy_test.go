package harness

import (
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"computer-use-server/internal/store"
)

func privacyForTest(t *testing.T, projects ...Project) *Privacy {
	t.Helper()
	p, err := NewPrivacy(filepath.Join(t.TempDir(), "privacy.json"), func() []Project { return projects })
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Set(PrivacySettings{Enabled: true, Words: []PrivacyWord{{Name: "code", Value: "bluebird"}}}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPrivacyMasksWholeValuesOnly(t *testing.T) {
	p := privacyForTest(t, Project{ID: "1", Name: "my-proj", Path: "/work/proj"}, Project{ID: "2", Name: "inner", Path: "/work/proj/inner"})
	for in, want := range map[string]string{
		"/work/proj/x.go:3: bad":     "${RR_ROOT_MY_PROJ}/x.go:3: bad",
		"/work/proj/inner/y":         "${RR_ROOT_INNER}/y",
		"cd /work/proj":              "cd ${RR_ROOT_MY_PROJ}",
		"in /work/proj.":             "in ${RR_ROOT_MY_PROJ}.",
		`"/work/proj"`:               `"${RR_ROOT_MY_PROJ}"`,
		"/work/proj2 /work/proj.old": "/work/proj2 /work/proj.old",
		"a bluebird; bluebirds":      "a ${RR_CODE}; bluebirds",
	} {
		if got := p.Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
		if back := p.Expand(p.Redact(in)); back != in {
			t.Errorf("round trip of %q gave %q", in, back)
		}
	}
	if err := p.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if got := p.Redact("/work/proj/x"); got != "/work/proj/x" {
		t.Fatalf("masked while off: %q", got)
	}
	// Tokens an agent saw before masking was turned off keep working.
	if got := p.Expand("${RR_ROOT_MY_PROJ}/x"); got != "/work/proj/x" {
		t.Fatalf("expand while off: %q", got)
	}
}

func TestPrivacySettingsPersistAndValidate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "privacy.json")
	p, err := NewPrivacy(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled() {
		t.Fatal("privacy mode must be on by default")
	}
	for _, bad := range []PrivacyWord{{Name: "1x", Value: "value"}, {Name: "ok", Value: "ab"}} {
		if err := p.Set(PrivacySettings{Enabled: false, Words: []PrivacyWord{bad}}); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if !p.Enabled() {
		t.Fatal("a rejected change was applied")
	}
	// A saved choice to turn it off is kept.
	if err := p.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if off, err := NewPrivacy(file, nil); err != nil || off.Enabled() {
		t.Fatalf("saved off was not kept: %v", err)
	}
	if err := p.Set(PrivacySettings{Enabled: true, MaskHost: true, Words: []PrivacyWord{{Name: "team", Value: "acme-corp"}}}); err != nil {
		t.Fatal(err)
	}
	again, err := NewPrivacy(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := again.Settings(); !s.Enabled || !s.MaskHost || len(s.Words) != 1 || s.Words[0].Name != "TEAM" {
		t.Fatalf("reloaded settings: %+v", s)
	}
}

func TestPrivacyHoldsBackASplitValue(t *testing.T) {
	p := privacyForTest(t, Project{ID: "1", Name: "proj", Path: "/work/proj"})
	b := newStreamBuf("", "x")
	b.privacy = p
	b.Write([]byte("see /work/pr"))
	if text, _ := b.Drain(false); text != "see " {
		t.Fatalf("first read: %q", text)
	}
	b.Write([]byte("oj/main.go\n"))
	if text, _ := b.Drain(false); text != "/work/proj/main.go\n" {
		t.Fatalf("second read: %q", text)
	}
	// A finished process returns everything.
	b.Write([]byte("/work/p"))
	if text, _ := b.Drain(true); text != "/work/p" {
		t.Fatalf("final read: %q", text)
	}
}

// privacyProject approves one temporary folder with privacy mode on.
func privacyProject(t *testing.T) (*Registry, *Privacy, string) {
	t.Helper()
	projects, err := NewProjects(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	root := projects.Snapshot().Projects[0].Path
	r := registryForTest(t)
	p, err := NewPrivacy("", func() []Project { return projects.Snapshot().Projects })
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	r.Privacy = p
	f, err := NewFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	f.Projects = projects
	f.Register(r)
	projects.Register(r)
	proc := NewProcesses(root)
	proc.Projects, proc.Privacy = projects, p
	t.Cleanup(proc.Stop)
	proc.Register(r)
	r.Enable("terminal", true)
	return r, p, root
}

func leaks(t *testing.T, out Output, root string) bool {
	t.Helper()
	b, _ := json.Marshal(out.Value)
	return strings.Contains(string(b), root) || strings.Contains(out.Text, root)
}

func TestPrivacyToolsRoundTripFilesUnchanged(t *testing.T) {
	r, _, root := privacyProject(t)
	original := "config = \"" + root + "/data\"\nother = 1\n"
	if err := os.WriteFile(filepath.Join(root, "app.toml"), []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := invoke(t, r, "read_file", map[string]any{"path": "app.toml"})
	if err != nil || leaks(t, out, root) {
		t.Fatalf("read_file leaked the root: %v %+v", err, out)
	}
	content, _ := asMap(t, out)["content"].(string)
	if !strings.Contains(content, tokenPrefix) {
		t.Fatalf("content not masked: %q", content)
	}
	// Writing back what was read restores the real path.
	if out, err = invoke(t, r, "write_file", map[string]any{"path": "app.toml", "content": content + "\n"}); err != nil || leaks(t, out, root) {
		t.Fatalf("write_file: %v %+v", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "app.toml")); string(b) != original {
		t.Fatalf("file changed:\n%s", b)
	}
	line := strings.SplitN(content, "\n", 2)[0]
	if out, err = invoke(t, r, "edit_file", map[string]any{"path": "app.toml", "old_string": line, "new_string": strings.Replace(line, "/data", "/cache", 1)}); err != nil || leaks(t, out, root) {
		t.Fatalf("edit_file: %v %+v", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "app.toml")); !strings.Contains(string(b), root+"/cache") {
		t.Fatalf("edit through a token failed:\n%s", b)
	}
}

func TestPrivacyMasksCommandsListsAndErrors(t *testing.T) {
	r, p, root := privacyProject(t)
	out, err := invoke(t, r, "exec_command", map[string]any{"command": "pwd; echo \"$PWD\" >&2", "yield_time_ms": 5000})
	if err != nil || leaks(t, out, root) || !strings.Contains(out.Text, tokenPrefix) {
		t.Fatalf("exec_command: %v %+v", err, out)
	}
	// A token in a command is expanded before the shell sees it.
	token := p.Redact(root)
	out, err = invoke(t, r, "exec_command", map[string]any{"command": "test -d '" + token + "' && echo found", "yield_time_ms": 5000})
	if err != nil || !strings.Contains(out.Text, "found") {
		t.Fatalf("token in command: %v %q", err, out.Text)
	}
	if out, err = invoke(t, r, "list_projects", map[string]any{}); err != nil || leaks(t, out, root) {
		t.Fatalf("list_projects: %v %+v", err, out)
	}
	_, err = invoke(t, r, "exec_command", map[string]any{"command": "true", "cwd": root + "/missing"})
	if err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("error leaked the root: %v", err)
	}
	// The audit log keeps real values for the local console; the public console
	// masks it as it serves it (see the server tests).
	calls, _, err := r.store.List(store.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) == 0 || !strings.Contains(string(calls[len(calls)-1].Result), root) {
		t.Fatal("the audit log should record the real working directory")
	}
}

func TestPrivacyMasksEvents(t *testing.T) {
	r := registryForTest(t)
	r.Privacy = privacyForTest(t, Project{ID: "1", Name: "proj", Path: "/work/proj"})
	r.Publish(Event{Session: "s", Kind: "task_finished", Data: map[string]any{"text": "done in /work/proj"}})
	if got := r.TakeNotices("s"); len(got) != 1 || got[0].Data["text"] != "done in ${RR_ROOT_PROJ}" {
		t.Fatalf("notice: %+v", got)
	}
}

func TestPrivacyCoversEscapedPathsUserAndScreenshotPaths(t *testing.T) {
	p := privacyForTest(t, Project{ID: "1", Name: "proj", Path: "/work/proj"})
	in := `{"file":"\/work\/proj\/a.go"}`
	if got := p.Redact(in); got != `{"file":"${RR_ROOT_PROJ_ESCAPED}\/a.go"}` || p.Expand(got) != in {
		t.Fatalf("escaped path: %q", got)
	}
	v := p.RedactValue(map[string]any{"screenshot": "/work/proj/shot.png", "other": map[string]any{"type": "image", "data": "/work/proj"}}).(map[string]any)
	if v["screenshot"] != "${RR_ROOT_PROJ}/shot.png" || v["other"].(map[string]any)["data"] != "/work/proj" {
		t.Fatalf("screenshot path or image data: %+v", v)
	}
	// Privacy mode, with the user and host names, is on by default.
	fresh, err := NewPrivacy(filepath.Join(t.TempDir(), "privacy.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := fresh.Settings(); !s.MaskUser || !s.MaskHost || !s.Enabled {
		t.Fatalf("defaults: %+v", s)
	}
}

func TestPrivacyHoldsAFragmentUntilTheProcessEnds(t *testing.T) {
	p := privacyForTest(t, Project{ID: "1", Name: "proj", Path: "/work/proj"})
	b := newStreamBuf("", "x")
	b.privacy = p
	b.Write([]byte("prompt /work/pr"))
	b.last = b.last.Add(-time.Hour) // silent for a long time
	if text, _ := b.Drain(false); text != "prompt " {
		t.Fatalf("released a fragment: %q", text)
	}
	if text, _ := b.Drain(true); text != "/work/pr" {
		t.Fatalf("final read: %q", text)
	}
}

func TestPrivacyMasksTheUserNameInEncodedAndCompoundText(t *testing.T) {
	u, err := user.Current()
	if err != nil || len(u.Username) < 4 || strings.ContainsAny(u.Username, `\`) {
		t.Skip("needs a plain user name of four or more characters")
	}
	name := u.Username
	p, err := NewPrivacy("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"owner " + name + " staff", `"ls\n` + name + ` x"`, `>` + name, name + "s-projects", "github.com/" + name + "/repo"} {
		got := p.Redact(in)
		if strings.Contains(got, name) || p.Expand(got) != in {
			t.Errorf("Redact(%q) = %q", in, got)
		}
	}
	// Custom words stay whole-word only.
	if err := p.Set(PrivacySettings{Enabled: true, Words: []PrivacyWord{{Name: "code", Value: "bluebird"}}}); err != nil {
		t.Fatal(err)
	}
	if got := p.Redact("bluebirds"); got != "bluebirds" {
		t.Fatalf("custom word inside a longer word: %q", got)
	}
}

func TestPrivacyEscapesLiteralTokenText(t *testing.T) {
	p := privacyForTest(t, Project{ID: "1", Name: "proj", Path: "/work/proj"})
	// A file that documents tokens and also holds a real path.
	in := "use HOMETOK here; data in /work/proj/data; cost $5"
	in = strings.ReplaceAll(in, "HOMETOK", "${RR_HOME}")
	got := p.Redact(in)
	if got != "use ${RR_DOLLAR}{RR_HOME} here; data in ${RR_ROOT_PROJ}/data; cost $5" {
		t.Fatalf("Redact = %q", got)
	}
	if back := p.Expand(got); back != in {
		t.Fatalf("round trip changed the text: %q", back)
	}
	// An agent writes a literal token with the dollar escape.
	if got := p.Expand("${RR_DOLLAR}{RR_HOME}"); got != "${RR_HOME}" {
		t.Fatalf("literal token: %q", got)
	}
	// A literal token split across two reads of a stream is held back too.
	b := newStreamBuf("", "x")
	b.privacy = p
	b.Write([]byte("see $"))
	if text, _ := b.Drain(false); text != "see " {
		t.Fatalf("first read: %q", text)
	}
	b.Write([]byte("{RR_HOME}"))
	if text, _ := b.Drain(true); text != "${RR_HOME}" {
		t.Fatalf("second read: %q", text)
	}
}

// masked reports whether out is masked exactly once: no real root, and no token
// escaped as though it were literal text.
func maskedOnce(t *testing.T, out Output, root string) {
	t.Helper()
	b, _ := json.Marshal(out.Value)
	all := string(b) + out.Text
	if strings.Contains(all, root) || strings.Contains(all, "DOLLAR") || !strings.Contains(all, tokenPrefix) {
		t.Fatalf("not masked exactly once: %s", all)
	}
}

func TestPrivacyMasksExactlyOnce(t *testing.T) {
	r, p, root := privacyProject(t)
	r.RegisterHelp()
	out, err := invoke(t, r, "exec_command", map[string]any{"command": "pwd", "yield_time_ms": 5000})
	if err != nil {
		t.Fatal(err)
	}
	maskedOnce(t, out, root)
	// Calls run by batch and use_tool are masked by the outer call only, and the
	// token in the inner arguments is expanded once.
	inner := map[string]any{"command": "cd '" + p.Redact(root) + "' && pwd", "yield_time_ms": 5000}
	out, err = invoke(t, r, "batch", map[string]any{"calls": []any{map[string]any{"tool": "exec_command", "arguments": inner}}})
	if err != nil {
		t.Fatal(err)
	}
	maskedOnce(t, out, root)
	out, err = invoke(t, r, "use_tool", map[string]any{"name": "exec_command", "arguments": inner})
	if err != nil {
		t.Fatal(err)
	}
	maskedOnce(t, out, root)
	// A background job's notice is masked once too.
	if _, err = invoke(t, r, "exec_command", map[string]any{"command": "sleep 0.3; pwd; echo $PWD/x >&2; exit 3", "background": true}); err != nil {
		t.Fatal(err)
	}
	r.WaitBackground()
	notices := r.TakeNotices("test-session")
	b, _ := json.Marshal(notices)
	if len(notices) == 0 || strings.Contains(string(b), root) || strings.Contains(string(b), "DOLLAR") {
		t.Fatalf("notices: %s", b)
	}
}

func TestPrivacyLongOutputCutKeepsNoFragment(t *testing.T) {
	p := privacyForTest(t, Project{ID: "1", Name: "proj", Path: "/work/proj"})
	line := "/work/proj/x\n"
	for shift := 0; shift < len(line); shift++ {
		data := []byte(strings.Repeat("a", shift) + strings.Repeat(line, 4000))
		text, cut := elide(data, responseCap, headKeep, 0, p)
		if !cut {
			t.Fatal("expected a cut")
		}
		for _, part := range strings.Split(p.Redact(text), "\n") {
			if strings.Contains(part, "work") || strings.Contains(part, "proj") && !strings.Contains(part, tokenPrefix) {
				t.Fatalf("shift %d left a fragment: %q", shift, part)
			}
		}
	}
}
