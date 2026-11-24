package chromemcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// cdpConn is one WebSocket to Chrome's browser debugging endpoint. Pages are
// reached through flattened sessions on the same socket, so Chrome asks the
// user to allow the connection once, not once per tab.
type cdpConn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[int64]chan cdpReply
	seq     int64
	done    chan struct{}
	once    sync.Once
	// onEvent runs on the reading goroutine: it must return quickly and must
	// never wait for a reply from Chrome.
	onEvent func(session, method string, params json.RawMessage)
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *cdpError) Error() string { return e.Message }

type cdpReply struct {
	Result json.RawMessage
	Err    *cdpError
}

var errChromeClosed = errors.New("Chrome closed the debugging connection")

// dialCDP opens the connection. ctx limits the handshake only, which in
// Chrome's approval mode lasts until the user answers the permission dialog.
func dialCDP(ctx context.Context, endpoint string, onEvent func(session, method string, params json.RawMessage)) (*cdpConn, error) {
	// No proxy for a loopback address, and no Origin header: Chrome refuses origins it was not started with.
	ws, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: &http.Client{Transport: &http.Transport{Proxy: nil}}})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(1 << 30) // screenshots and response bodies arrive as one message
	c := &cdpConn{ws: ws, pending: map[int64]chan cdpReply{}, done: make(chan struct{}), onEvent: onEvent}
	go c.read()
	return c, nil
}

func (c *cdpConn) read() {
	defer c.Close()
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			return
		}
		var msg struct {
			ID        int64           `json:"id"`
			Method    string          `json:"method"`
			Params    json.RawMessage `json:"params"`
			Result    json.RawMessage `json:"result"`
			Error     *cdpError       `json:"error"`
			SessionID string          `json:"sessionId"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg.Method != "" {
			if c.onEvent != nil {
				c.onEvent(msg.SessionID, msg.Method, msg.Params)
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[msg.ID]
		delete(c.pending, msg.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- cdpReply{Result: msg.Result, Err: msg.Error}
		}
	}
}

// Close drops the socket. Chrome and its pages are left as they are.
func (c *cdpConn) Close() {
	c.once.Do(func() { close(c.done); _ = c.ws.CloseNow() })
}

func (c *cdpConn) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// call sends one command, to the browser when session is empty, and waits for its reply.
func (c *cdpConn) call(ctx context.Context, session, method string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ch := make(chan cdpReply, 1)
	c.mu.Lock()
	c.seq++
	id := c.seq
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if session != "" {
		msg["sessionId"] = session
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	// The library closes the socket when a write's context ends, and a new socket
	// means a new permission dialog, so the write does not use the caller's context.
	writeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	c.writeMu.Lock()
	err = c.ws.Write(writeCtx, websocket.MessageText, data)
	c.writeMu.Unlock()
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.Close()
		return nil, errChromeClosed
	}
	select {
	case reply := <-ch:
		if reply.Err != nil {
			return nil, fmt.Errorf("%s: %w", method, reply.Err)
		}
		return reply.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, errChromeClosed
	}
}

// into decodes a reply into v.
func into(raw json.RawMessage, err error, v any) error {
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
