// Package browser is the runtime's own Chrome DevTools Protocol client.
//
// It talks to Chromium directly: one WebSocket to the browser endpoint, browser
// contexts for session isolation, flattened sessions for per-context commands and
// events, and Performance.getMetrics for per-session cost. No intermediary sits
// between this package and Chromium.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// CDPError is an error the browser reported for one command. It carries the code
// Chromium sent, so a caller can tell "no such target" from "not allowed".
type CDPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *CDPError) Error() string { return fmt.Sprintf("cdp %d: %s", e.Code, e.Message) }

// ErrClosed is returned for commands issued after the connection ended.
var ErrClosed = errors.New("cdp: connection closed")

// Event is a browser or session event Chromium pushed.
type Event struct {
	Method    string
	SessionID string
	Params    json.RawMessage
}

type reply struct {
	result json.RawMessage
	err    error
}

// Conn is one WebSocket to a Chromium browser endpoint, demultiplexing replies
// and events from a single reader.
type Conn struct {
	ws *websocket.Conn

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan reply
	sessions map[string]*Session
	events   chan Event
	closed   bool
	err      error
	done     chan struct{}
}

// Dial opens a connection to a browser WebSocket URL (ws://host:port/devtools/browser/<id>).
func Dial(ctx context.Context, wsURL string) (*Conn, error) {
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		// Chromium rejects requests whose Origin is not allowed. A runtime client
		// is not a browser page, so send none.
		HTTPHeader: nil,
	})
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", wsURL, err)
	}
	ws.SetReadLimit(32 << 20)

	conn := &Conn{
		ws:       ws,
		pending:  map[int64]chan reply{},
		sessions: map[string]*Session{},
		events:   make(chan Event, 256),
		done:     make(chan struct{}),
	}
	go conn.readLoop()
	return conn, nil
}

// Events is the browser-level event stream. It closes when the connection ends.
func (c *Conn) Events() <-chan Event { return c.events }

// Err reports why the connection ended, once it has.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close shuts the connection down.
func (c *Conn) Close() error {
	c.finish(ErrClosed)
	return c.ws.Close(websocket.StatusNormalClosure, "client closing")
}

// Do sends one command and waits for its reply.
func (c *Conn) Do(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return c.do(ctx, method, params, "")
}

// do sends a command, optionally on a flattened session.
func (c *Conn) do(ctx context.Context, method string, params any, sessionID string) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = ErrClosed
		}
		return nil, err
	}
	c.nextID++
	id := c.nextID
	wait := make(chan reply, 1)
	c.pending[id] = wait
	c.mu.Unlock()

	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	if sessionID != "" {
		payload["sessionId"] = sessionID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		c.forget(id)
		return nil, err
	}

	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.ws.Write(writeCtx, websocket.MessageText, body); err != nil {
		c.forget(id)
		c.finish(err)
		return nil, fmt.Errorf("%s: write: %w", method, err)
	}

	select {
	case <-ctx.Done():
		c.forget(id)
		return nil, ctx.Err()
	case result := <-wait:
		return result.result, result.err
	}
}

func (c *Conn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// finish records why the connection ended and releases every waiter.
func (c *Conn) finish(cause error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.err = cause
	waiters := make([]chan reply, 0, len(c.pending))
	for id, ch := range c.pending {
		waiters = append(waiters, ch)
		delete(c.pending, id)
	}
	close(c.done)
	c.mu.Unlock()

	for _, ch := range waiters {
		ch <- reply{err: cause}
	}
	c.mu.Lock()
	sessions := make([]*Session, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session)
	}
	c.sessions = map[string]*Session{}
	c.mu.Unlock()
	for _, session := range sessions {
		session.shutdown()
	}
	close(c.events)
}

// readLoop fans messages out to waiters and to the event stream.
func (c *Conn) readLoop() {
	for {
		_, body, err := c.ws.Read(context.Background())
		if err != nil {
			c.finish(fmt.Errorf("cdp: read: %w", err))
			return
		}
		var message struct {
			ID        int64           `json:"id"`
			Result    json.RawMessage `json:"result"`
			Error     *CDPError       `json:"error"`
			Method    string          `json:"method"`
			SessionID string          `json:"sessionId"`
			Params    json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &message); err != nil {
			continue
		}
		if message.ID != 0 {
			c.mu.Lock()
			wait, ok := c.pending[message.ID]
			delete(c.pending, message.ID)
			c.mu.Unlock()
			if ok {
				var failure error
				if message.Error != nil {
					failure = message.Error
				}
				select {
				case wait <- reply{result: message.Result, err: failure}:
				default:
				}
			}
			continue
		}
		if message.Method == "" {
			continue
		}
		event := Event{Method: message.Method, SessionID: message.SessionID, Params: message.Params}
		if message.SessionID != "" {
			// A session event belongs to exactly one session; handing it to a
			// shared channel would let one session consume another's events.
			c.mu.Lock()
			session := c.sessions[message.SessionID]
			c.mu.Unlock()
			if session != nil {
				session.deliver(event)
			}
			continue
		}
		select {
		case c.events <- event:
		default:
			// A full event buffer drops rather than blocking the reader: a stuck
			// consumer must not stall command replies.
		}
	}
}
