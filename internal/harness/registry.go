// Package harness separates model-visible specifications from execution and auditing.
package harness

import (
	"bytes"
	"computer-use-server/internal/store"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

type Spec struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Category     string         `json:"category"`
	InputSchema  map[string]any `json:"inputSchema"`
	Mutating     bool           `json:"mutating"`
	Parallel     bool           `json:"parallel"`
	Annotations  map[string]any `json:"annotations,omitempty"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	// Group "advanced" tools stay callable (directly or through use_tool) but are
	// left out of tools/list; help lists them. Empty means a core tool.
	Group string `json:"group,omitempty"`
	// Lock names the resource a non-parallel tool serialises on; the default is
	// its category, so a slow browser call never blocks a file write.
	Lock string `json:"-"`
}
type Invocation struct {
	ID, Session, Client string
	Arguments           json.RawMessage
}
type Output struct {
	Value      any
	Screenshot string
	Completion func() (any, error)
	Snapshot   func() any
	Cancel     context.CancelFunc
	MCPResult  map[string]any
	// Text is returned to MCP clients as a plain text block in place of the
	// Value keys listed in TextKeys, so bodies are not JSON-escaped.
	Text     string
	TextKeys []string
	// Images are returned as image blocks; they are never written to the audit log.
	Images []Image
	// Failure marks the audit record as failed while the call still returns its
	// result normally (a command that exited non-zero is data, not a tool error).
	Failure string
}

// Image is a base64 image returned to the agent.
type Image struct {
	MIME string `json:"mimeType"`
	Data string `json:"data"`
}

// ToolError carries a stable machine-readable code next to the message.
type ToolError struct{ Code, Message string }

func (e *ToolError) Error() string { return e.Message }

// ErrorCode classifies err for API responses.
func ErrorCode(err error) string {
	var te *ToolError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &te):
		return te.Code
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, fs.ErrNotExist):
		return "not_found"
	case errors.Is(err, fs.ErrPermission):
		return "permission_denied"
	case errors.Is(err, fs.ErrExist):
		return "file_exists"
	}
	return "tool_error"
}

// describedError rewords a low-level error for the agent and the activity log
// while errors.Is/As (and so ErrorCode) still see the original.
type describedError struct {
	msg string
	err error
}

func (e *describedError) Error() string { return e.msg }
func (e *describedError) Unwrap() error { return e.err }

// describe replaces Go's raw "context canceled" and "openat x: ..." texts.
// clientGone reports whether the caller's own context ended.
func describe(err error, clientGone bool, elapsed time.Duration) error {
	var te *ToolError
	// Only a bare path error: a wrapped one already carries its own context.
	pe, isPath := err.(*fs.PathError)
	switch {
	case err == nil, errors.As(err, &te):
		return err
	case errors.Is(err, context.Canceled):
		reason := "stopped from ReadyRig (cancel request, pause or capability turned off)"
		if clientGone {
			reason = "the client disconnected or abandoned the call"
		}
		return &describedError{fmt.Sprintf("cancelled after %s: %s", humanDuration(elapsed.Milliseconds()), reason), err}
	case isPath:
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return &describedError{"no such file or directory: " + pe.Path, err}
		case errors.Is(err, fs.ErrPermission):
			return &describedError{"permission denied: " + pe.Path, err}
		case errors.Is(err, fs.ErrExist):
			return &describedError{"already exists: " + pe.Path, err}
		}
		return &describedError{fmt.Sprintf("%s: %v", pe.Path, pe.Err), err}
	}
	return err
}

// Event is a server-initiated notification for one agent session.
type Event struct {
	Session string         `json:"-"`
	Kind    string         `json:"kind"`
	Data    map[string]any `json:"data"`
}
type subscription struct {
	session string
	ch      chan Event
}
type Handler func(context.Context, Invocation) (Output, error)
type Tool struct {
	Spec Spec
	Run  Handler
	// Upstream MCP servers validate their complete JSON Schema themselves.
	External bool
	// Informational tools can describe blocked capabilities while control is paused.
	AvailableWhenPaused bool
}
type activeCall struct {
	cancel   context.CancelFunc
	category string
}

type Registry struct {
	background    sync.WaitGroup
	foreground    sync.WaitGroup
	mu            sync.Mutex
	tools         map[string]Tool
	order         []string
	active        map[string]activeCall
	paused        bool
	lastStarted   time.Time
	enabled       map[string]bool
	locks         map[string]chan struct{}
	listGen       chan struct{}
	subs          map[int]subscription
	pending       map[string][]Event
	progress      []func(session string) []Event
	progressSent  map[string]time.Time
	progressEvery time.Duration
	nextSub       int
	// PermissionCheck reports an operating-system permission a tool still lacks
	// (empty when it can run). Tools that lack one fail fast with permission_required.
	PermissionCheck func(Spec) string
	// ExposeAll lists advanced tools in tools/list too.
	ExposeAll bool
	store     *store.Store
	notify    chan struct{}
	secretsMu sync.RWMutex
	secrets   []string
	OnPause   func()
}

func New(s *store.Store, secrets ...string) *Registry {
	return &Registry{tools: map[string]Tool{}, active: map[string]activeCall{}, enabled: map[string]bool{"system": true, "files": true, "terminal": false, "computer": false, "browser": true, "safari": true}, locks: map[string]chan struct{}{}, listGen: make(chan struct{}), subs: map[int]subscription{}, pending: map[string][]Event{}, progressSent: map[string]time.Time{}, store: s, notify: make(chan struct{}), secrets: secrets}
}

// SessionOrDefault is the session name a call without one is recorded under.
func SessionOrDefault(session string) string {
	if session == "" {
		return "default"
	}
	return session
}
func ID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[t.Spec.Name]; ok {
		panic("duplicate tool " + t.Spec.Name)
	}
	if t.Spec.Mutating && !t.External {
		addDescription(t.Spec.InputSchema)
	}
	r.tools[t.Spec.Name] = t
	r.order = append(r.order, t.Spec.Name)
	r.signalList()
}

// addDescription gives every mutating tool an optional free-text intent that
// is recorded in the audit log and removed before the handler runs.
func addDescription(schema map[string]any) {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return
	}
	if _, exists := props["description"]; !exists {
		props["description"] = Prop("string", "Activity-log label (optional)")
	}
}
func stripDescription(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	if _, ok := m["description"]; !ok {
		return raw
	}
	delete(m, "description")
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return b
}

// ListedSpecs is what tools/list advertises: enabled capabilities, minus the
// advanced group unless ExposeAll is set.
func (r *Registry) ListedSpecs() []Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Spec{}
	for _, n := range r.order {
		t := r.tools[n]
		if !r.enabled[t.Spec.Category] || t.Spec.Group == "advanced" && !r.ExposeAll {
			continue
		}
		out = append(out, t.Spec)
	}
	return out
}

// ToolListChanged is closed whenever the advertised tool list may have changed.
func (r *Registry) ToolListChanged() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listGen
}
func (r *Registry) signalList() {
	// Callers hold r.mu or are single-threaded during setup.
	close(r.listGen)
	r.listGen = make(chan struct{})
}

// Subscribe delivers events for one session until cancel is called.
func (r *Registry) Subscribe(session string) (<-chan Event, func()) {
	r.mu.Lock()
	id := r.nextSub
	r.nextSub++
	ch := make(chan Event, 32)
	r.subs[id] = subscription{session, ch}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.subs, id)
		r.mu.Unlock()
	}
}

// Publish never blocks. A session with a live event stream gets the event
// there; otherwise it is held (at most 32 per session) until that session's
// next call collects it with TakeNotices, so plain request/response clients
// still learn that a background job finished.
func (r *Registry) Publish(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	live := false
	for _, s := range r.subs {
		if s.session == e.Session {
			live = true
			select {
			case s.ch <- e:
			default:
			}
		}
	}
	if live {
		return
	}
	if _, known := r.pending[e.Session]; !known && len(r.pending) >= 512 {
		return
	}
	q := append(r.pending[e.Session], e)
	if len(q) > 32 {
		q = q[len(q)-32:]
	}
	r.pending[e.Session] = q
}

// AddProgress registers a source of "still running" status for a session. Its
// events (kind task_progress) are attached to results by ProgressFor.
func (r *Registry) AddProgress(fn func(session string) []Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, fn)
}

// SetProgressInterval changes how often a session is shown progress (default 15 s).
func (r *Registry) SetProgressInterval(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progressEvery = d
}

// progressSkip lists tools whose own result already reports running work.
var progressSkip = map[string]bool{"write_stdin": true, "list_tasks": true, "help": true}

// ProgressFor is the pull-based substitute for MCP progress notifications: any
// tool result in a session can carry a short status line for jobs that session
// has still running, at most once per interval, with no streaming connection.
func (r *Registry) ProgressFor(session, tool string) []Event {
	if progressSkip[tool] {
		return nil
	}
	r.mu.Lock()
	fns := append([]func(string) []Event(nil), r.progress...)
	every := r.progressEvery
	if every == 0 {
		every = 15 * time.Second
	}
	last := r.progressSent[session]
	r.mu.Unlock()
	if time.Since(last) < every {
		return nil
	}
	var out []Event
	for _, fn := range fns {
		out = append(out, fn(session)...)
	}
	if len(out) == 0 {
		return nil
	}
	r.mu.Lock()
	if _, known := r.progressSent[session]; !known && len(r.progressSent) >= 512 {
		for k := range r.progressSent {
			delete(r.progressSent, k)
			break
		}
	}
	r.progressSent[session] = time.Now()
	r.mu.Unlock()
	if len(out) > 5 {
		extra := len(out) - 5
		out = append(out[:5], Event{Session: session, Kind: "task_progress", Data: map[string]any{"text": fmt.Sprintf("+%d more running tasks; list_tasks shows all", extra)}})
	}
	return out
}

// labelled gives a mutating call that has no description a short one derived
// from its arguments, so the activity log reads well without the model's help.
func labelled(t Tool, raw json.RawMessage) json.RawMessage {
	if !t.Spec.Mutating || t.External {
		return raw
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return raw
	}
	if d, _ := m["description"].(string); d != "" {
		return raw
	}
	label := deriveLabel(t.Spec.Name, m)
	if label == "" {
		return raw
	}
	m["description"] = label
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return b
}
func deriveLabel(tool string, m map[string]any) string {
	s := func(k string) string { v, _ := m[k].(string); return v }
	short := func(v string, n int) string {
		v = strings.Join(strings.Fields(v), " ")
		if len(v) > n {
			v = strings.ToValidUTF8(v[:n], "") + "…"
		}
		return v
	}
	switch tool {
	case "exec_command":
		return short(s("command"), 90)
	case "write_file":
		return "write " + s("path")
	case "edit_file":
		return "edit " + s("path")
	case "write_stdin":
		switch {
		case m["terminate"] == true:
			return "terminate command session"
		case s("chars") != "":
			return "send input to command session"
		}
		return "poll command session"
	case "computer_action":
		if a, ok := m["actions"].([]any); ok {
			return fmt.Sprintf("%d desktop actions", len(a))
		}
		return s("action")
	case "computer_app":
		return short(s("action")+" "+s("app"), 60)
	case "computer_clipboard":
		return s("action") + " clipboard"
	case "use_tool":
		return "run " + s("name")
	case "batch":
		if c, ok := m["calls"].([]any); ok {
			return fmt.Sprintf("batch of %d calls", len(c))
		}
	}
	return ""
}

// WithoutOwn drops events about the command session a result already
// describes: progress for it, and its finish when the result says it ended.
func WithoutOwn(events []Event, value any) []Event {
	m, ok := value.(map[string]any)
	if !ok {
		return events
	}
	id, _ := m["session_id"].(string)
	if id == "" {
		return events
	}
	out := make([]Event, 0, len(events))
	for _, e := range events {
		if sid, _ := e.Data["session_id"].(string); sid == id && (e.Kind == "task_progress" || m["running"] == false) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// TakeNotices returns and clears the events held for a session.
func (r *Registry) TakeNotices(session string) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.pending[session]
	delete(r.pending, session)
	return q
}
func (r *Registry) lock(key string) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.locks[key]
	if !ok {
		l = make(chan struct{}, 1)
		r.locks[key] = l
	}
	return l
}
func (t Tool) lockKey() string {
	if t.Spec.Parallel {
		return ""
	}
	if t.Spec.Lock != "" {
		return t.Spec.Lock
	}
	return t.Spec.Category
}
func (r *Registry) Specs() []Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Spec{}
	for _, n := range r.order {
		out = append(out, r.tools[n].Spec)
	}
	return out
}

// ReplaceCategory atomically publishes (or withdraws) discovered tools.
func (r *Registry) ReplaceCategory(category string, tools []Tool) {
	r.ReplaceTools(category, "", tools)
}

// ReplaceTools replaces the tools of one provider in a category: those whose names
// start with prefix. Two browsers can share the browser category this way.
func (r *Registry) ReplaceTools(category, prefix string, tools []Tool) {
	r.mu.Lock()
	order := make([]string, 0, len(r.order)+len(tools))
	for _, name := range r.order {
		if r.tools[name].Spec.Category == category && strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
		} else {
			order = append(order, name)
		}
	}
	for _, t := range tools {
		if t.Spec.Category != category || !strings.HasPrefix(t.Spec.Name, prefix) {
			r.mu.Unlock()
			panic("tool category mismatch")
		}
		if _, exists := r.tools[t.Spec.Name]; exists {
			r.mu.Unlock()
			panic("duplicate tool")
		}
		r.tools[t.Spec.Name] = t
		order = append(order, t.Spec.Name)
	}
	r.order = order
	r.signalList()
	r.mu.Unlock()
	r.Signal()
}
func (r *Registry) State() (bool, map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := map[string]bool{}
	for k, v := range r.enabled {
		e[k] = v
	}
	return r.paused, e
}

// Activity is a lightweight snapshot for the native menu bar; it avoids database polling.
func (r *Registry) Activity() (paused bool, running int, lastStarted time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused, len(r.active), r.lastStarted
}
func (r *Registry) Changed() <-chan struct{} { r.mu.Lock(); defer r.mu.Unlock(); return r.notify }
func (r *Registry) Signal() {
	r.mu.Lock()
	close(r.notify)
	r.notify = make(chan struct{})
	r.mu.Unlock()
}
func (r *Registry) SetPaused(p bool) {
	r.mu.Lock()
	r.paused = p
	if p {
		for _, active := range r.active {
			active.cancel()
		}
		if r.OnPause != nil {
			r.OnPause()
		}
	}
	r.mu.Unlock()
	r.Signal()
}
func (r *Registry) Enable(category string, v bool) error {
	if category == "system" {
		return errors.New("system help is always enabled")
	}
	r.mu.Lock()
	if _, ok := r.enabled[category]; !ok {
		r.mu.Unlock()
		return errors.New("unknown category")
	}
	changed := r.enabled[category] != v
	r.enabled[category] = v
	if changed {
		r.signalList()
	}
	if !v {
		for _, active := range r.active {
			if active.category == category {
				active.cancel()
			}
		}
	}
	r.mu.Unlock()
	r.Signal()
	return nil
}

// Cancel affects only this invocation, including a yielded command.
func (r *Registry) Cancel(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	active, ok := r.active[id]
	if ok {
		active.cancel()
	}
	return ok
}
func (r *Registry) finishActive(id string) {
	r.mu.Lock()
	if active, ok := r.active[id]; ok {
		active.cancel()
		delete(r.active, id)
	}
	r.mu.Unlock()
}
func (r *Registry) Invoke(ctx context.Context, name string, in Invocation) (out Output, call store.Call, err error) {
	r.foreground.Add(1)
	defer r.foreground.Done()
	if in.ID == "" {
		in.ID = ID()
	}
	in.Session = SessionOrDefault(in.Session)
	if in.Client == "" {
		in.Client = "Remote agent"
	}
	r.mu.Lock()
	t, ok := r.tools[name]
	r.mu.Unlock()
	call = store.Call{ID: in.ID, Session: in.Session, Client: in.Client, Tool: name, Category: t.Spec.Category, Status: "running", Started: time.Now(), Arguments: r.redact(labelled(t, in.Arguments))}
	if err = r.store.Save(call); err != nil {
		return out, call, fmt.Errorf("audit log unavailable: %w", err)
	}
	r.Signal()
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("tool panic: %v", p)
		}
		call.Duration = time.Since(call.Started).Milliseconds()
		err = describe(err, parent.Err() != nil, time.Since(call.Started))
		if err != nil {
			if call.Status != "denied" {
				call.Status = "error"
				if errors.Is(err, context.Canceled) {
					call.Status = "cancelled"
				}
			}
			call.Error = r.redactText(err.Error())
		} else {
			call.Status = "success"
			if out.Completion != nil {
				call.Status = "running"
			} else if out.Failure != "" {
				call.Status = "error"
				call.Error = r.redactText(out.Failure)
			}
		}
		b, e := json.Marshal(out.Value)
		if e != nil {
			err = e
			call.Status = "error"
			call.Error = e.Error()
		}
		call.Result = r.redact(b)
		call.Screenshot = out.Screenshot
		if e := r.store.Save(call); e != nil {
			err = fmt.Errorf("persist result: %w", e)
		}
		if out.Completion != nil && err == nil {
			r.mu.Lock()
			// Cancellation may arrive between the handler yielding and this handover.
			if out.Cancel != nil {
				if ctx.Err() != nil || r.paused || !r.enabled[t.Spec.Category] {
					out.Cancel()
				}
				r.active[in.ID] = activeCall{out.Cancel, t.Spec.Category}
			}
			r.mu.Unlock()
			r.background.Add(1)
			go r.follow(call, out)
		} else {
			if out.Cancel != nil {
				out.Cancel()
			}
			r.finishActive(in.ID)
		}
		r.Signal()
	}()
	if !ok {
		err = &ToolError{"unknown_tool", "unknown tool"}
		return
	}
	if t.External {
		var args map[string]any
		if json.Unmarshal(in.Arguments, &args) != nil || args == nil {
			err = errors.New("arguments must be a JSON object")
		}
	} else {
		err = validate(in.Arguments, t.Spec.InputSchema)
	}
	if err != nil {
		err = &ToolError{"invalid_arguments", err.Error()}
		return
	}
	r.mu.Lock()
	if !r.enabled[t.Spec.Category] {
		r.mu.Unlock()
		call.Status = "denied"
		err = &ToolError{"capability_disabled", "the " + t.Spec.Category + " capability is disabled; enable it in the local console"}
		return
	}
	if r.paused && !t.AvailableWhenPaused {
		r.mu.Unlock()
		call.Status = "denied"
		err = &ToolError{"control_paused", "control is paused; resume it in the local console"}
		return
	}
	r.active[in.ID] = activeCall{cancel, t.Spec.Category}
	r.lastStarted = time.Now()
	r.mu.Unlock()
	if r.PermissionCheck != nil {
		if msg := r.PermissionCheck(t.Spec); msg != "" {
			call.Status = "denied"
			err = &ToolError{"permission_required", msg}
			return
		}
	}
	if key := t.lockKey(); key != "" {
		lock := r.lock(key)
		select {
		case lock <- struct{}{}:
			defer func() { <-lock }()
		case <-ctx.Done():
			err = ctx.Err()
			return
		}
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if t.Spec.Mutating && !t.External {
		in.Arguments = stripDescription(in.Arguments)
	}
	out, err = t.Run(ctx, in)
	return
}
func (r *Registry) follow(saved store.Call, out Output) {
	defer r.background.Done()
	defer r.finishActive(saved.ID)
	type result struct {
		value any
		err   error
	}
	done := make(chan result, 1)
	go func() {
		var res result
		defer func() {
			if p := recover(); p != nil {
				res.err = fmt.Errorf("completion panic: %v", p)
			}
			done <- res
		}()
		res.value, res.err = out.Completion()
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var final any
	for {
		select {
		case <-ticker.C:
			if out.Snapshot == nil {
				continue
			}
			b, err := json.Marshal(out.Snapshot())
			if err != nil {
				continue
			}
			if bytes.Equal(saved.Result, r.redact(b)) {
				continue
			}
			saved.Result = r.redact(b)
			saved.Duration = time.Since(saved.Started).Milliseconds()
		case res := <-done:
			saved.Duration = time.Since(saved.Started).Milliseconds()
			saved.Status = "success"
			if res.err != nil {
				res.err = describe(res.err, false, time.Since(saved.Started))
				saved.Status = "error"
				saved.Error = r.redactText(res.err.Error())
				if errors.Is(res.err, context.Canceled) {
					saved.Status = "cancelled"
				}
			}
			b, err := json.Marshal(res.value)
			if err != nil {
				saved.Status = "error"
				saved.Error = err.Error()
			}
			saved.Result = r.redact(b)
			final = res.value
		}
		if err := r.store.Save(saved); err != nil {
			log.Printf("persist background result: %v", err)
		}
		r.Signal()
		if saved.Status != "running" {
			r.Publish(finishedEvent(saved, final))
			return
		}
	}
}

// finishedEvent summarises a background call without including its output.
func finishedEvent(c store.Call, final any) Event {
	data := map[string]any{"call_id": c.ID, "tool": c.Tool, "status": c.Status, "duration_ms": c.Duration}
	if m, ok := final.(map[string]any); ok {
		for _, k := range []string{"session_id", "exit_code", "timed_out", "cancelled", "terminated", "stdout_path", "stderr_path"} {
			if v, ok := m[k]; ok {
				data[k] = v
			}
		}
	}
	return Event{Session: c.Session, Kind: "task_finished", Data: data}
}
func (r *Registry) AddSecrets(secrets ...string) {
	r.secretsMu.Lock()
	defer r.secretsMu.Unlock()
	r.secrets = append(r.secrets, secrets...)
}

func (r *Registry) redactText(s string) string {
	r.secretsMu.RLock()
	defer r.secretsMu.RUnlock()
	for _, secret := range r.secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
		}
	}
	return s
}
func (r *Registry) redact(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage(`null`)
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return json.RawMessage(`null`)
	}
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				lower := strings.ToLower(k)
				if sensitiveKey(lower) {
					x[k] = "[REDACTED]"
				} else if k == "screenshot" && strings.HasPrefix(fmt.Sprint(val), "data:") {
					x[k] = "[stored as screenshot]"
				} else {
					x[k] = clean(val)
				}
			}
			return x
		case []any:
			for i := range x {
				x[i] = clean(x[i])
			}
			return x
		case string:
			return r.redactText(x)
		default:
			return v
		}
	}
	b, _ = json.Marshal(clean(v))
	return b
}

// sensitiveKey matches credential-like keys but not counters such as max_tokens.
func sensitiveKey(lower string) bool {
	switch {
	case strings.Contains(lower, "password"), strings.Contains(lower, "secret"), strings.Contains(lower, "passwd"):
		return true
	case lower == "authorization", lower == "api_key", lower == "apikey", lower == "token":
		return true
	}
	return strings.HasSuffix(lower, "_token") || strings.HasSuffix(lower, "token") && !strings.HasSuffix(lower, "tokens")
}
func Decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected a single JSON object")
	}
	return nil
}
func Schema(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func Prop(kind, description string) map[string]any {
	return map[string]any{"type": kind, "description": description}
}
func validate(raw []byte, schema map[string]any) error {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return errors.New("arguments must be a JSON object")
	}
	props := schema["properties"].(map[string]any)
	// Unknown names come first: a misspelt required argument (cmd for command)
	// is the real mistake, not the missing one.
	var unknown []string
	for key := range value {
		if _, ok := props[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return unknownArgument(unknown[0], props)
	}
	for _, key := range schema["required"].([]string) {
		if _, ok := value[key]; !ok {
			return fmt.Errorf("missing argument: %s", key)
		}
	}
	for key, v := range value {
		p := props[key]
		kind := p.(map[string]any)["type"]
		valid := false
		switch kind {
		case "string":
			_, valid = v.(string)
		case "number", "integer":
			n, ok := v.(float64)
			valid = ok && (kind != "integer" || n == float64(int64(n)))
		case "boolean":
			_, valid = v.(bool)
		case "array":
			_, valid = v.([]any)
		case "object":
			_, valid = v.(map[string]any)
		}
		if !valid {
			return fmt.Errorf("%s must be %s", key, kind)
		}
		if err := checkConstraints(key, v, p.(map[string]any)); err != nil {
			return err
		}
	}
	return nil
}

// unknownArgument names the closest valid argument, or lists them all.
func unknownArgument(key string, props map[string]any) error {
	names := make([]string, 0, len(props))
	best, bestScore := "", 3
	for name := range props {
		names = append(names, name)
		score := editDistance(strings.ToLower(key), strings.ToLower(name))
		if isAbbreviation(strings.ToLower(key), strings.ToLower(name)) {
			score = 1
		}
		if score < bestScore || score == bestScore && name < best {
			best, bestScore = name, score
		}
	}
	if best != "" {
		return fmt.Errorf("unknown argument: %s (did you mean %s?)", key, best)
	}
	sort.Strings(names)
	return fmt.Errorf("unknown argument: %s (valid: %s)", key, strings.Join(names, ", "))
}

// isAbbreviation reports whether short keeps name's first letter and the rest
// of its letters in order, as cmd does for command.
func isAbbreviation(short, name string) bool {
	if len(short) < 2 || len(short) >= len(name) || short[0] != name[0] {
		return false
	}
	i := 0
	for j := 0; j < len(name) && i < len(short); j++ {
		if name[j] == short[i] {
			i++
		}
	}
	return i == len(short)
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// checkConstraints enforces the schema keywords the tools declare: enum,
// minimum/maximum and array size and element types.
func checkConstraints(key string, v any, p map[string]any) error {
	if values, ok := p["enum"].([]string); ok {
		s, _ := v.(string)
		found := false
		for _, want := range values {
			found = found || s == want
		}
		if !found {
			return fmt.Errorf("%s must be one of: %s", key, strings.Join(values, ", "))
		}
	}
	if n, ok := v.(float64); ok {
		if min, ok := p["minimum"].(int); ok && n < float64(min) {
			return fmt.Errorf("%s must be at least %d", key, min)
		}
		if max, ok := p["maximum"].(int); ok && n > float64(max) {
			return fmt.Errorf("%s must be at most %d", key, max)
		}
	}
	if items, ok := v.([]any); ok {
		if min, ok := p["minItems"].(int); ok && len(items) < min {
			return fmt.Errorf("%s needs at least %d items", key, min)
		}
		if max, ok := p["maxItems"].(int); ok && len(items) > max {
			return fmt.Errorf("%s allows at most %d items", key, max)
		}
		if spec, ok := p["items"].(map[string]any); ok {
			for _, item := range items {
				valid := true
				switch spec["type"] {
				case "string":
					_, valid = item.(string)
				case "number", "integer":
					_, valid = item.(float64)
				case "object":
					_, valid = item.(map[string]any)
				}
				if !valid {
					return fmt.Errorf("%s items must be %v", key, spec["type"])
				}
			}
		}
	}
	return nil
}

// WaitBackground is called after stopping the gateway and all processes.
func (r *Registry) WaitBackground() { r.foreground.Wait(); r.background.Wait() }
