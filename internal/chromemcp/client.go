package chromemcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// client talks to an MCP server child process (Safari's safaridriver --mcp).
// stdio uses one JSON-RPC message per line. Responses are routed by id;
// server notifications may arrive between responses.
type client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writer  sync.Mutex
	mu      sync.Mutex
	pending map[int64]chan response
	seq     atomic.Int64
	done    chan struct{}
	exited  chan struct{}
	once    sync.Once
	cancel  context.CancelFunc
}
type response struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func startClient(parent context.Context, path string, args, env []string) (*client, error) {
	ctx, cancel := context.WithCancel(parent)
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.WaitDelay = time.Second
	c := &client{cmd: cmd, pending: map[int64]chan response{}, done: make(chan struct{}), exited: make(chan struct{}), cancel: cancel}
	configureProcess(cmd)
	var err error
	if c.stdin, err = cmd.StdinPipe(); err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.stdin.Close()
		cancel()
		return nil, err
	}
	// Upstream stderr can contain page information; it is not written to the app's logs.
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		stdout.Close()
		c.stdin.Close()
		cancel()
		return nil, err
	}
	go func() {
		defer c.Close()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 65536), 32<<20)
		for scanner.Scan() {
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				response
			}
			if json.Unmarshal(scanner.Bytes(), &msg) != nil {
				continue
			}
			if msg.Method != "" {
				// A request or notification from the server. Its id is the server's
				// own and must never be matched against our pending calls.
				go c.serve(msg.ID, msg.Method)
				continue
			}
			var id int64
			if json.Unmarshal(msg.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- msg.response:
				default:
				}
			}
		}
	}()
	go func() { _ = cmd.Wait(); c.Close(); close(c.exited) }()
	return c, nil
}
func (c *client) Close() { c.once.Do(func() { close(c.done); c.cancel(); c.stdin.Close() }) }
func (c *client) write(v any) error {
	c.writer.Lock()
	defer c.writer.Unlock()
	return json.NewEncoder(c.stdin).Encode(v)
}
func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, c.Close)
	defer stop()
	id := c.seq.Add(1)
	ch := make(chan response, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("MCP (%d): %s", msg.Error.Code, msg.Error.Message)
		}
		return msg.Result, nil
	case <-ctx.Done():
		// Closing our MCP child releases the connection; never terminate the user's browser.
		c.Close()
		return nil, ctx.Err()
	case <-c.done:
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Safari MCP 已断开")
	}
}

// serve answers what the server asks of us.
func (c *client) serve(id json.RawMessage, method string) {
	if len(id) == 0 || string(id) == "null" {
		return // a notification needs no reply
	}
	switch {
	case method == "ping":
		c.reply(id, map[string]any{}, nil)
	default:
		c.reply(id, nil, map[string]any{"code": -32601, "message": "method not found"})
	}
}
func (c *client) reply(id json.RawMessage, result any, rpcError map[string]any) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcError != nil {
		msg["error"] = rpcError
	} else {
		msg["result"] = result
	}
	_ = c.write(msg)
}

func (c *client) initialize(ctx context.Context) error {
	raw, err := c.call(ctx, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "ReadyRig", "version": "0.4.0"}})
	if err != nil {
		return err
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(raw, &result) != nil || (result.ProtocolVersion != "2025-06-18" && result.ProtocolVersion != "2025-03-26" && result.ProtocolVersion != "2025-11-25" && result.ProtocolVersion != "2024-11-05") {
		return errors.New("Safari MCP 返回了不支持的协议版本")
	}
	return c.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
}
