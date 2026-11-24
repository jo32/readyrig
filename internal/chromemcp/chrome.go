package chromemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"computer-use-server/internal/harness"
)

// chrome is the state of one debugging connection: the pages ReadyRig has
// numbered, and for each page it has used, what Chrome reported about it.
type chrome struct {
	conn     *cdpConn
	endpoint string
	roots    func() []string

	mu        sync.Mutex
	ids       map[string]int // target id -> page id, stable for the connection
	nextID    int
	pages     map[string]*page // attached pages by target id
	sessions  map[string]*page // attached pages by session id
	selected  string           // target id of the page used when pageId is omitted
	contexts  map[string]string
	attaching sync.Mutex
}

const (
	maxConsole  = 1000
	maxRequests = 2000
	// keptNavigations is how many earlier navigations the "preserved" options reach back.
	keptNavigations = 3
)

type dialogInfo struct {
	Kind, Message, Default string
}

type consoleMsg struct {
	id    int
	gen   int
	kind  string
	text  string
	stack string
}

type netRequest struct {
	id          int
	gen         int
	requestID   string
	method, url string
	kind        string
	headers     map[string]any
	postData    string
	hasPost     bool
	responded   bool
	status      int
	statusText  string
	mime        string
	respHeaders map[string]any
	finished    bool
	failure     string
}

type page struct {
	c       *chrome
	id      int
	target  string
	session string

	mu      sync.Mutex
	changed chan struct{} // closed and replaced whenever the fields below change
	loading bool
	gen     int  // counts main-frame navigations
	navReq  bool // the document request of the next navigation was already counted
	dialog  *dialogInfo
	// autoDialog answers dialogs while a tool that expects them runs:
	// "accept", "dismiss", or any other text to accept a prompt with it.
	autoDialog string
	// unloadOnly limits autoDialog to the "leave this page?" dialog of a navigation.
	unloadOnly bool
	console    []consoleMsg
	nextMsg    int
	requests   []*netRequest
	byRequest  map[string]*netRequest
	nextReq    int
	snapshot   int
	uids       map[int]map[string]int64 // snapshot number -> uid -> backend DOM node id
	chooser    chan int64
	drag       chan json.RawMessage
}

func newChrome(conn *cdpConn, roots func() []string) *chrome {
	return &chrome{conn: conn, roots: roots, ids: map[string]int{}, pages: map[string]*page{}, sessions: map[string]*page{}, contexts: map[string]string{}}
}

func toolError(format string, args ...any) error {
	return &harness.ToolError{Code: "browser_tool_failed", Message: fmt.Sprintf(format, args...)}
}

// event runs on the connection's reading goroutine.
func (c *chrome) event(session, method string, params json.RawMessage) {
	if session == "" {
		var gone struct {
			TargetID  string `json:"targetId"`
			SessionID string `json:"sessionId"`
		}
		if method != "Target.targetDestroyed" && method != "Target.detachedFromTarget" || json.Unmarshal(params, &gone) != nil {
			return
		}
		c.mu.Lock()
		p := c.sessions[gone.SessionID]
		if p == nil {
			p = c.pages[gone.TargetID]
		}
		if p != nil {
			delete(c.sessions, p.session)
			delete(c.pages, p.target)
		}
		if method == "Target.targetDestroyed" && c.selected == gone.TargetID {
			c.selected = ""
		}
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	p := c.sessions[session]
	c.mu.Unlock()
	if p != nil {
		p.event(method, params)
	}
}

type targetInfo struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	ID       int    `json:"-"`
}

// targets lists the open tabs, giving each one a page id that stays the same
// while this connection lasts.
func (c *chrome) targets(ctx context.Context) ([]targetInfo, error) {
	var reply struct {
		TargetInfos []targetInfo `json:"targetInfos"`
	}
	raw, err := c.conn.call(ctx, "", "Target.getTargets", nil)
	if err = into(raw, err, &reply); err != nil {
		return nil, err
	}
	var out []targetInfo
	c.mu.Lock()
	for _, t := range reply.TargetInfos {
		if t.Type != "page" || strings.HasPrefix(t.URL, "devtools://") || strings.HasPrefix(t.URL, "chrome-extension://") {
			continue
		}
		if c.ids[t.TargetID] == 0 {
			c.nextID++
			c.ids[t.TargetID] = c.nextID
		}
		t.ID = c.ids[t.TargetID]
		out = append(out, t)
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// page returns the attached page for a page id, or the selected page when id is nil.
func (c *chrome) page(ctx context.Context, id *int) (*page, error) {
	list, err := c.targets(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	selected := c.selected
	c.mu.Unlock()
	var found *targetInfo
	for i := range list {
		if id != nil && list[i].ID == *id || id == nil && list[i].TargetID == selected {
			found = &list[i]
		}
	}
	if found == nil {
		if id == nil {
			return nil, toolError("No page is selected. Pass pageId; chrome_list_pages lists the open pages")
		}
		return nil, toolError("No page with pageId %d. chrome_list_pages lists the open pages", *id)
	}
	return c.attach(ctx, found.TargetID, found.ID)
}

func (c *chrome) attach(ctx context.Context, target string, id int) (*page, error) {
	c.attaching.Lock()
	defer c.attaching.Unlock()
	c.mu.Lock()
	p := c.pages[target]
	c.mu.Unlock()
	if p != nil {
		return p, nil
	}
	var reply struct {
		SessionID string `json:"sessionId"`
	}
	raw, err := c.conn.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target, "flatten": true})
	if err = into(raw, err, &reply); err != nil {
		return nil, err
	}
	p = &page{c: c, id: id, target: target, session: reply.SessionID, changed: make(chan struct{}), byRequest: map[string]*netRequest{}, chooser: make(chan int64, 1), drag: make(chan json.RawMessage, 1)}
	c.mu.Lock()
	c.pages[target] = p
	c.sessions[p.session] = p
	c.mu.Unlock()
	for _, domain := range []string{"Page", "Runtime", "Network", "Log", "DOM"} {
		if _, err = p.call(ctx, domain+".enable", nil); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (c *chrome) pageID(target string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ids[target] == 0 {
		c.nextID++
		c.ids[target] = c.nextID
	}
	return c.ids[target]
}

func (c *chrome) selectTarget(target string) {
	c.mu.Lock()
	c.selected = target
	c.mu.Unlock()
}

func (p *page) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return p.c.conn.call(ctx, p.session, method, params)
}

// notify wakes everything waiting on the page. The caller holds p.mu.
func (p *page) notify() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// wait blocks until cond holds, reporting false when limit or ctx ends first.
// cond runs with p.mu held.
func (p *page) wait(ctx context.Context, limit time.Duration, cond func() bool) bool {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		p.mu.Lock()
		ok, changed := cond(), p.changed
		p.mu.Unlock()
		if ok {
			return true
		}
		select {
		case <-changed:
		case <-timer.C:
			return false
		case <-ctx.Done():
			return false
		case <-p.c.conn.done:
			return false
		}
	}
}

func pause(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// settle gives the page time to react to an action: a navigation it started
// is awaited, up to limit, unless a dialog is waiting for an answer.
func (p *page) settle(ctx context.Context, limit time.Duration) bool {
	pause(ctx, 120*time.Millisecond)
	done := p.wait(ctx, limit, func() bool { return !p.loading || p.dialog != nil })
	if done {
		pause(ctx, 50*time.Millisecond)
	}
	return done
}

// act sends an input command. Chrome holds some replies back while a dialog
// is open, so an opened dialog also ends the wait.
func (p *page) act(ctx context.Context, method string, params any) error {
	done := make(chan error, 1)
	go func() {
		_, err := p.call(ctx, method, params)
		done <- err
	}()
	for {
		p.mu.Lock()
		open, changed := p.dialog != nil, p.changed
		p.mu.Unlock()
		if open {
			return nil
		}
		select {
		case err := <-done:
			return err
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// event runs on the connection's reading goroutine.
func (p *page) event(method string, params json.RawMessage) {
	switch method {
	case "Page.frameStartedLoading", "Page.frameStoppedLoading":
		var e struct {
			FrameID string `json:"frameId"`
		}
		if json.Unmarshal(params, &e) == nil && e.FrameID == p.target {
			p.mu.Lock()
			p.loading = method == "Page.frameStartedLoading"
			p.notify()
			p.mu.Unlock()
		}
	case "Page.frameNavigated":
		var e struct {
			Frame struct {
				ParentID string `json:"parentId"`
			} `json:"frame"`
		}
		if json.Unmarshal(params, &e) == nil && e.Frame.ParentID == "" {
			p.mu.Lock()
			if p.navReq {
				p.navReq = false
			} else {
				p.navigated()
			}
			p.uids = nil
			p.notify()
			p.mu.Unlock()
		}
	case "Page.javascriptDialogOpening":
		var e struct {
			Message       string `json:"message"`
			Type          string `json:"type"`
			DefaultPrompt string `json:"defaultPrompt"`
		}
		if json.Unmarshal(params, &e) != nil {
			return
		}
		p.mu.Lock()
		auto := p.autoDialog
		if p.unloadOnly && e.Type != "beforeunload" {
			auto = ""
		}
		if auto == "" {
			p.dialog = &dialogInfo{Kind: e.Type, Message: e.Message, Default: e.DefaultPrompt}
			p.notify()
		}
		p.mu.Unlock()
		if auto != "" {
			answer := map[string]any{"accept": auto != "dismiss"}
			if auto != "accept" && auto != "dismiss" {
				answer["promptText"] = auto
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, _ = p.call(ctx, "Page.handleJavaScriptDialog", answer)
			}()
		}
	case "Page.javascriptDialogClosed":
		p.mu.Lock()
		p.dialog = nil
		p.notify()
		p.mu.Unlock()
	case "Page.fileChooserOpened":
		var e struct {
			BackendNodeID int64 `json:"backendNodeId"`
		}
		if json.Unmarshal(params, &e) == nil {
			select {
			case p.chooser <- e.BackendNodeID:
			default:
			}
		}
	case "Input.dragIntercepted":
		var e struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(params, &e) == nil {
			select {
			case p.drag <- e.Data:
			default:
			}
		}
	case "Runtime.consoleAPICalled", "Runtime.exceptionThrown", "Log.entryAdded":
		p.consoleEvent(method, params)
	case "Network.requestWillBeSent", "Network.responseReceived", "Network.loadingFinished", "Network.loadingFailed":
		p.networkEvent(method, params)
	}
}

// navigated starts a new generation of console messages and requests, dropping
// those older than the preserved navigations. The caller holds p.mu.
func (p *page) navigated() {
	p.gen++
	oldest := p.gen - keptNavigations
	for len(p.console) > 0 && p.console[0].gen < oldest {
		p.console = p.console[1:]
	}
	for len(p.requests) > 0 && p.requests[0].gen < oldest {
		if old := p.requests[0]; p.byRequest[old.requestID] == old {
			delete(p.byRequest, old.requestID)
		}
		p.requests = p.requests[1:]
	}
}

type remoteObject struct {
	Type                string          `json:"type"`
	Subtype             string          `json:"subtype"`
	Value               json.RawMessage `json:"value"`
	UnserializableValue string          `json:"unserializableValue"`
	Description         string          `json:"description"`
	ObjectID            string          `json:"objectId"`
	Preview             *struct {
		Overflow   bool `json:"overflow"`
		Properties []struct {
			Name  string `json:"name"`
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"properties"`
	} `json:"preview"`
}

func (o remoteObject) text() string {
	if len(o.Value) > 0 {
		var s string
		if json.Unmarshal(o.Value, &s) == nil {
			return s
		}
		return string(o.Value)
	}
	if o.UnserializableValue != "" {
		return o.UnserializableValue
	}
	if o.Preview != nil && o.Subtype != "error" {
		// What the console shows for an object: its first properties.
		parts := make([]string, 0, len(o.Preview.Properties)+1)
		for _, property := range o.Preview.Properties {
			value := property.Value
			if property.Type == "string" {
				value = strconv.Quote(value)
			}
			if o.Subtype == "array" {
				parts = append(parts, value)
			} else {
				parts = append(parts, property.Name+": "+value)
			}
		}
		if o.Preview.Overflow {
			parts = append(parts, "…")
		}
		if o.Subtype == "array" {
			return "[" + strings.Join(parts, ", ") + "]"
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	if o.Description != "" {
		return o.Description
	}
	return o.Type
}

type stackTrace struct {
	CallFrames []struct {
		FunctionName string `json:"functionName"`
		URL          string `json:"url"`
		LineNumber   int    `json:"lineNumber"`
		ColumnNumber int    `json:"columnNumber"`
	} `json:"callFrames"`
}

func (s *stackTrace) String() string {
	if s == nil {
		return ""
	}
	var sb strings.Builder
	for i, f := range s.CallFrames {
		if i == 20 {
			break
		}
		name := f.FunctionName
		if name == "" {
			name = "(anonymous)"
		}
		fmt.Fprintf(&sb, "    at %s (%s:%d:%d)\n", name, f.URL, f.LineNumber+1, f.ColumnNumber+1)
	}
	return sb.String()
}

type exceptionDetails struct {
	Text       string        `json:"text"`
	Exception  *remoteObject `json:"exception"`
	StackTrace *stackTrace   `json:"stackTrace"`
}

func (e *exceptionDetails) message() string {
	if e.Exception != nil {
		if text := e.Exception.text(); text != "" && text != e.Exception.Type {
			return text
		}
	}
	return e.Text
}

func (p *page) consoleEvent(method string, params json.RawMessage) {
	msg := consoleMsg{}
	switch method {
	case "Runtime.consoleAPICalled":
		var e struct {
			Type       string         `json:"type"`
			Args       []remoteObject `json:"args"`
			StackTrace *stackTrace    `json:"stackTrace"`
		}
		if json.Unmarshal(params, &e) != nil {
			return
		}
		parts := make([]string, len(e.Args))
		for i, a := range e.Args {
			parts[i] = a.text()
		}
		msg.kind, msg.text, msg.stack = e.Type, strings.Join(parts, " "), e.StackTrace.String()
		if msg.kind == "warning" {
			msg.kind = "warn"
		}
	case "Runtime.exceptionThrown":
		var e struct {
			ExceptionDetails exceptionDetails `json:"exceptionDetails"`
		}
		if json.Unmarshal(params, &e) != nil {
			return
		}
		msg.kind, msg.text, msg.stack = "error", "Uncaught "+e.ExceptionDetails.message(), e.ExceptionDetails.StackTrace.String()
	case "Log.entryAdded":
		var e struct {
			Entry struct {
				Level      string      `json:"level"`
				Text       string      `json:"text"`
				URL        string      `json:"url"`
				StackTrace *stackTrace `json:"stackTrace"`
			} `json:"entry"`
		}
		if json.Unmarshal(params, &e) != nil {
			return
		}
		msg.kind, msg.text, msg.stack = e.Entry.Level, e.Entry.Text, e.Entry.StackTrace.String()
		if msg.kind == "warning" {
			msg.kind = "warn"
		}
		if e.Entry.URL != "" {
			msg.text += " (" + e.Entry.URL + ")"
		}
	}
	p.mu.Lock()
	p.nextMsg++
	msg.id, msg.gen = p.nextMsg, p.gen
	p.console = append(p.console, msg)
	if len(p.console) > maxConsole {
		p.console = p.console[len(p.console)-maxConsole:]
	}
	p.mu.Unlock()
}

func (p *page) networkEvent(method string, params json.RawMessage) {
	var e struct {
		RequestID string `json:"requestId"`
		LoaderID  string `json:"loaderId"`
		FrameID   string `json:"frameId"`
		Type      string `json:"type"`
		ErrorText string `json:"errorText"`
		Request   struct {
			URL         string         `json:"url"`
			Method      string         `json:"method"`
			Headers     map[string]any `json:"headers"`
			PostData    string         `json:"postData"`
			HasPostData bool           `json:"hasPostData"`
		} `json:"request"`
		Response, RedirectResponse *struct {
			Status     int            `json:"status"`
			StatusText string         `json:"statusText"`
			Headers    map[string]any `json:"headers"`
			MimeType   string         `json:"mimeType"`
		}
	}
	if json.Unmarshal(params, &e) != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.byRequest[e.RequestID]
	switch method {
	case "Network.requestWillBeSent":
		if r != nil && e.RedirectResponse != nil {
			r.responded, r.finished = true, true
			r.status, r.statusText, r.respHeaders, r.mime = e.RedirectResponse.Status, e.RedirectResponse.StatusText, e.RedirectResponse.Headers, e.RedirectResponse.MimeType
		} else if e.Type == "Document" && e.FrameID == p.target && e.RequestID == e.LoaderID {
			// The request that loads a new document belongs to the page it loads.
			p.navigated()
			p.navReq = true
		}
		p.nextReq++
		r = &netRequest{id: p.nextReq, gen: p.gen, requestID: e.RequestID, method: e.Request.Method, url: e.Request.URL, kind: strings.ToLower(e.Type), headers: e.Request.Headers, postData: e.Request.PostData, hasPost: e.Request.HasPostData}
		p.byRequest[e.RequestID] = r
		p.requests = append(p.requests, r)
		for len(p.requests) > maxRequests {
			if old := p.requests[0]; p.byRequest[old.requestID] == old {
				delete(p.byRequest, old.requestID)
			}
			p.requests = p.requests[1:]
		}
	case "Network.responseReceived":
		if r != nil && e.Response != nil {
			r.responded = true
			r.status, r.statusText, r.respHeaders, r.mime = e.Response.Status, e.Response.StatusText, e.Response.Headers, e.Response.MimeType
		}
	case "Network.loadingFinished":
		if r != nil {
			r.finished = true
		}
	case "Network.loadingFailed":
		if r != nil {
			r.finished, r.failure = true, e.ErrorText
		}
	}
}
