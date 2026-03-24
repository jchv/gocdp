package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// On is a type-safe helper to register a global event handler.
//
// Because Go does not allow type parameters on methods, this is a top-level
// function rather than a method on Connection.
//
// Usage:
//
//	hid := core.On(conn, func(sessionID string, ev *cdp.PageLoadEventFiredEvent) {
//	    fmt.Println("page loaded at", ev.Timestamp)
//	})
//	defer conn.RemoveHandler(hid)
func On[E any, PE interface {
	*E
	Event
}](c *Connection, cb func(sessionID string, event PE)) HandlerID {
	var zero E
	name := PE(&zero).CDPEventName()

	return c.AddHandler(name, func(sessionID string, params json.RawMessage) {
		var ev E
		if err := json.Unmarshal(params, &ev); err != nil {
			slog.Error("failed to decode CDP event", slog.String("event", name), slog.Any("err", err))
			return
		}
		cb(sessionID, &ev)
	})
}

// Command represents a CDP protocol command that can be sent over a connection.
type Command interface {
	CDPMethodName() string
}

// Event represents a CDP protocol event received from the browser.
type Event interface {
	CDPEventName() string
}

// Transaction represents a pending CDP command.
type Transaction struct {
	ID        int64
	Method    string
	Params    any
	SessionID string
}

// RequestMsg is the JSON-serializable structure sent over the websocket for a CDP command.
type RequestMsg struct {
	ID        int64  `json:"id"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

// ErrorMsg represents an error returned by the browser in response to a CDP command.
type ErrorMsg struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

// ResponseMsg is the JSON-serializable structure received over the websocket,
// representing either a command response or an event notification.
type ResponseMsg struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *ErrorMsg       `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

// HandlerID is an opaque identifier returned by AddHandler, used to remove
// the handler later via RemoveHandler.
type HandlerID struct {
	method string
	index  int
}

type handlerEntry struct {
	cb      func(string, json.RawMessage)
	removed bool
}

// Connection manages a CDP websocket connection, dispatching commands and
// routing event notifications to registered handlers.
type Connection struct {
	URL        string
	ws         *websocket.Conn
	mu         sync.Mutex
	idCounter  int64
	txMap      map[int64]chan ResponseMsg
	handlers   map[string][]*handlerEntry
	handlersMu sync.RWMutex
	closed     atomic.Bool
	done       chan struct{} // closed when listen() exits
}

// NewConnection dials the given websocket URL and returns a ready-to-use
// Connection. A background goroutine is started to read incoming messages.
func NewConnection(wsURL string) (*Connection, error) {
	_, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("invalid websocket URL: %w", err)
	}
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to dial: %w", err)
	}

	c := &Connection{
		URL:      wsURL,
		ws:       ws,
		txMap:    make(map[int64]chan ResponseMsg),
		handlers: make(map[string][]*handlerEntry),
		done:     make(chan struct{}),
	}

	go c.listen()
	return c, nil
}

func (c *Connection) listen() {
	defer func() {
		// Drain all pending transactions so callers don't hang forever.
		c.mu.Lock()
		for id, ch := range c.txMap {
			ch <- ResponseMsg{
				Error: &ErrorMsg{Code: -1, Message: "connection closed"},
			}
			delete(c.txMap, id)
		}
		c.mu.Unlock()

		close(c.done)
	}()

	for {
		var msg ResponseMsg
		err := c.ws.ReadJSON(&msg)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && !c.closed.Load() {
				slog.Error("websocket read error", slog.Any("err", err))
			}
			break
		}

		if msg.ID != 0 {
			c.mu.Lock()
			ch, ok := c.txMap[msg.ID]
			if ok {
				delete(c.txMap, msg.ID)
			}
			c.mu.Unlock()
			if ok {
				ch <- msg
			}
		} else if msg.Method != "" {
			c.handlersMu.RLock()
			entries := c.handlers[msg.Method]
			// Take a snapshot of active callbacks under the read lock.
			var cbs []func(string, json.RawMessage)
			for _, entry := range entries {
				if !entry.removed {
					cbs = append(cbs, entry.cb)
				}
			}
			c.handlersMu.RUnlock()
			for _, cb := range cbs {
				go cb(msg.SessionID, msg.Params)
			}
		}
	}
}

// AddHandler registers a callback for a CDP event method. It returns a
// HandlerID that can be passed to RemoveHandler to unregister the callback.
func (c *Connection) AddHandler(method string, cb func(string, json.RawMessage)) HandlerID {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	entry := &handlerEntry{cb: cb}
	idx := len(c.handlers[method])
	c.handlers[method] = append(c.handlers[method], entry)
	return HandlerID{method: method, index: idx}
}

// RemoveHandler unregisters a previously added handler by its HandlerID.
func (c *Connection) RemoveHandler(id HandlerID) {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	entries := c.handlers[id.method]
	if id.index >= 0 && id.index < len(entries) {
		entries[id.index].removed = true
	}
}

// Send sends a CDP command over the connection and waits for the response.
// If ret is non-nil, the result payload is JSON-unmarshalled into it.
func (c *Connection) Send(ctx context.Context, cmd Command, sessionID string, ret any) error {
	id := atomic.AddInt64(&c.idCounter, 1)

	req := RequestMsg{
		ID:        id,
		Method:    cmd.CDPMethodName(),
		Params:    cmd,
		SessionID: sessionID,
	}

	ch := make(chan ResponseMsg, 1)

	c.mu.Lock()
	c.txMap[id] = ch

	err := c.ws.WriteJSON(req)
	if err != nil {
		delete(c.txMap, id)
		c.mu.Unlock()
		return fmt.Errorf("write error: %w", err)
	}
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.txMap, id)
		c.mu.Unlock()
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("cdp error %d: %s (%s)", resp.Error.Code, resp.Error.Message, resp.Error.Data)
		}
		if ret != nil && resp.Result != nil {
			if err := json.Unmarshal(resp.Result, ret); err != nil {
				return fmt.Errorf("unmarshal result error: %w", err)
			}
		}
		return nil
	}
}

// Done returns a channel that is closed when the connection's read loop exits,
// for example due to a websocket error or an explicit Close call.
func (c *Connection) Done() <-chan struct{} {
	return c.done
}

// Close closes the underlying websocket connection.
func (c *Connection) Close() error {
	c.closed.Store(true)
	if c.ws != nil {
		return c.ws.Close()
	}
	return nil
}
