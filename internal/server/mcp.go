package server

import (
	"bytes"
	"computer-use-server/internal/buildinfo"
	"computer-use-server/internal/harness"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type rpc struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

const mcpInstructions = "ReadyRig gives you a computer. Use read_file, edit_file, write_file, list_directory, glob and search_files for files, not cat/sed/grep/find. exec_command returns exit codes as data; long output is cut and saved (read_file at stdout_path). For long jobs use background=true: results then carry [progress] lines and a [notice] when it ends; list_tasks lists jobs; write_stdin with a long yield_time_ms waits for completion. batch runs several tools in one request. Desktop: take a fresh screenshot before coordinate actions and chain steps with actions[]. Tools in group advanced are not listed: see help compact, run with use_tool. exec_command is not sandboxed. Use ${RR_*} path tokens as-is."

// rpcKey identifies an in-flight request so notifications/cancelled can find it.
func rpcKey(sid string, id json.RawMessage) string {
	var compact bytes.Buffer
	if json.Compact(&compact, id) != nil {
		return sid + "|" + string(id)
	}
	return sid + "|" + compact.String()
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	var req rpc
	if !decode(w, r, &req) {
		return
	}
	reply := func(result any) { write(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) }
	fail := func(code int, message string) {
		id := req.ID
		if len(id) == 0 {
			id = json.RawMessage(`null`)
		}
		write(w, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		fail(-32600, "Invalid Request")
		return
	}
	if req.Method == "initialize" {
		if len(req.ID) == 0 {
			fail(-32600, "initialize needs an id")
			return
		}
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name string `json:"name"`
			} `json:"clientInfo"`
		}
		if json.Unmarshal(req.Params, &p) != nil {
			fail(-32602, "Invalid initialize params")
			return
		}
		version := "2025-06-18"
		if p.ProtocolVersion == "2025-03-26" {
			version = p.ProtocolVersion
		}
		id := harness.ID()
		s.mu.Lock()
		if s.sessions == nil {
			s.sessions = map[string]mcpSession{}
		}
		for key, v := range s.sessions {
			if time.Since(v.At) > 24*time.Hour {
				delete(s.sessions, key)
			}
		}
		if len(s.sessions) >= 256 {
			s.mu.Unlock()
			problem(w, 429, fmt.Errorf("MCP session limit reached"))
			return
		}
		s.sessions[id] = mcpSession{Client: p.ClientInfo.Name, At: time.Now()}
		s.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", id)
		reply(map[string]any{"protocolVersion": version, "serverInfo": map[string]string{"name": "ReadyRig", "version": buildinfo.Version}, "capabilities": map[string]any{"tools": map[string]bool{"listChanged": true}, "logging": map[string]any{}}, "instructions": mcpInstructions})
		return
	}
	sid := r.Header.Get("Mcp-Session-Id")
	s.mu.Lock()
	session, ok := s.sessions[sid]
	s.mu.Unlock()
	if !ok || time.Since(session.At) > 24*time.Hour {
		problem(w, 404, fmt.Errorf("MCP session expired; initialize again"))
		return
	}
	if len(req.ID) == 0 {
		if req.Method == "notifications/cancelled" {
			var p struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			if json.Unmarshal(req.Params, &p) == nil && len(p.RequestID) > 0 {
				s.mu.Lock()
				cancel := s.inflight[rpcKey(sid, p.RequestID)]
				s.mu.Unlock()
				if cancel != nil {
					cancel()
				}
			}
		}
		w.WriteHeader(202)
		return
	}
	switch req.Method {
	case "ping", "logging/setLevel":
		reply(map[string]any{})
	case "tools/list":
		out := []map[string]any{}
		for _, t := range s.Registry.ListedSpecs() {
			annotations := t.Annotations
			if annotations == nil {
				annotations = map[string]any{"readOnlyHint": !t.Mutating, "destructiveHint": t.Mutating, "openWorldHint": t.Category != "files"}
			}
			tool := map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema, "annotations": annotations}
			if t.OutputSchema != nil {
				tool["outputSchema"] = t.OutputSchema
			}
			out = append(out, tool)
		}
		reply(map[string]any{"tools": out})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			fail(-32602, "Invalid tool call")
			return
		}
		if len(p.Arguments) == 0 {
			p.Arguments = json.RawMessage(`{}`)
		}
		ctx, cancel := context.WithCancel(r.Context())
		key := rpcKey(sid, req.ID)
		s.mu.Lock()
		if s.inflight == nil {
			s.inflight = map[string]context.CancelFunc{}
		}
		s.inflight[key] = cancel
		s.mu.Unlock()
		out, call, err := s.Registry.Invoke(ctx, p.Name, harness.Invocation{Session: sid, Client: session.Client, Arguments: p.Arguments})
		s.mu.Lock()
		delete(s.inflight, key)
		s.mu.Unlock()
		cancel()
		notices := s.Registry.TakeNotices(sid)
		notices = harness.WithoutOwn(append(notices, s.Registry.ProgressFor(sid, p.Name)...), out.Value)
		if out.MCPResult != nil {
			// Preserve content blocks, structuredContent and isError exactly.
			if len(notices) > 0 {
				reply(withNotices(out.MCPResult, notices))
				return
			}
			reply(out.MCPResult)
			return
		}
		reply(map[string]any{"content": toolContent(out, call.Error, err, notices), "isError": err != nil})
	default:
		fail(-32601, "Method not found")
	}
}

// toolContent turns a result into MCP content blocks: images first, then the
// plain-text body when the tool produced one, then notices. A JSON block follows
// only when it carries something the text does not: the result of a tool that
// has no text form, or the error and its code.
func toolContent(out harness.Output, callError string, err error, notices []harness.Event) []map[string]any {
	content := []map[string]any{}
	for _, img := range out.Images {
		content = append(content, map[string]any{"type": "image", "mimeType": img.MIME, "data": img.Data})
	}
	result := out.Value
	if value, ok := out.Value.(map[string]any); ok {
		if shot, ok := value["screenshot"].(string); ok {
			content = append(content, map[string]any{"type": "image", "mimeType": "image/jpeg", "data": strings.TrimPrefix(shot, "data:image/jpeg;base64,")})
			delete(value, "screenshot")
		}
	}
	text := out.Text != ""
	if text {
		content = append(content, map[string]any{"type": "text", "text": out.Text})
	}
	if len(notices) > 0 {
		content = append(content, map[string]any{"type": "text", "text": noticeText(notices)})
	}
	var envelope map[string]any
	switch {
	case err != nil:
		envelope = map[string]any{"error": callError, "error_code": harness.ErrorCode(err)}
		if !text && result != nil {
			envelope["result"] = result
		}
	case !text:
		envelope = map[string]any{"result": result}
	}
	if envelope != nil {
		if len(notices) > 0 {
			envelope["notices"] = NoticeValues(notices)
		}
		b, _ := json.Marshal(envelope)
		content = append(content, map[string]any{"type": "text", "text": string(b)})
	}
	return content
}

// noticeText renders held events as lines an agent reads before the metadata.
func noticeText(events []harness.Event) string {
	var b strings.Builder
	for _, e := range events {
		if e.Kind == "task_progress" {
			fmt.Fprintf(&b, "[progress] %v\n", e.Data["text"])
			continue
		}
		fmt.Fprintf(&b, "[notice] background %v %v: %v", e.Data["tool"], e.Kind, e.Data["status"])
		if e.Data["terminated"] == true {
			b.WriteString(", terminated at the agent's request")
		} else if code, ok := e.Data["exit_code"]; ok {
			fmt.Fprintf(&b, ", exit code %v", code)
		}
		if id, ok := e.Data["session_id"]; ok {
			fmt.Fprintf(&b, " (session_id %v; read what it printed with write_stdin)", id)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// NoticeValues is the JSON form of held events.
func NoticeValues(events []harness.Event) []map[string]any {
	out := make([]map[string]any, 0, len(events))
	for _, e := range events {
		v := map[string]any{"kind": e.Kind}
		for k, val := range e.Data {
			v[k] = val
		}
		out = append(out, v)
	}
	return out
}

// withNotices adds the notice text to a result that must otherwise stay untouched.
func withNotices(result map[string]any, events []harness.Event) map[string]any {
	copied := make(map[string]any, len(result)+1)
	for k, v := range result {
		copied[k] = v
	}
	content, _ := result["content"].([]any)
	copied["content"] = append(append([]any{}, content...), map[string]any{"type": "text", "text": noticeText(events)})
	return copied
}

// mcpStream serves the optional server-to-client event stream (GET /mcp):
// tool-list changes and background task completions for this session.
func (s *Server) mcpStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") || !ok {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	sid := r.Header.Get("Mcp-Session-Id")
	s.mu.Lock()
	session, found := s.sessions[sid]
	s.mu.Unlock()
	if !found || time.Since(session.At) > 24*time.Hour {
		problem(w, 404, fmt.Errorf("MCP session expired; initialize again"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	events, unsubscribe := s.Registry.Subscribe(sid)
	defer unsubscribe()
	send := func(method string, params any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
		flusher.Flush()
	}
	// Take the change channel before announcing the stream so no change after
	// "connected" can be missed.
	changed := s.Registry.ToolListChanged()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-changed:
			changed = s.Registry.ToolListChanged()
			send("notifications/tools/list_changed", map[string]any{})
		case e := <-events:
			send("notifications/message", map[string]any{"level": "info", "logger": "readyrig", "data": map[string]any{"kind": e.Kind, "detail": e.Data}})
		case <-ticker.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
func (s *Server) openapi(w http.ResponseWriter, r *http.Request) {
	paths := map[string]any{}
	for _, tool := range s.Registry.Specs() {
		paths["/api/v1/tools/"+tool.Name] = map[string]any{"post": map[string]any{"operationId": tool.Name, "summary": tool.Description, "tags": []string{tool.Category}, "parameters": []map[string]any{{"name": "X-Session-ID", "in": "header", "schema": map[string]string{"type": "string"}}}, "requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": tool.InputSchema}}}, "responses": map[string]any{"200": map[string]any{"description": "Tool result, call_id and status"}, "422": map[string]any{"description": "Tool error"}, "423": map[string]any{"description": "Capability disabled or paused"}}}}
	}
	write(w, map[string]any{"openapi": "3.1.0", "info": map[string]string{"title": "ReadyRig Local Agent Adapter", "version": buildinfo.Version}, "paths": paths, "servers": []map[string]string{{"url": "/" + s.requestAccessPath(r), "description": "Access path for this connection"}}})
}
