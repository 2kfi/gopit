// Package wsconn wraps a gorilla WebSocket connection with a write mutex.
// gorilla forbids concurrent writers and concurrent readers; the agent has
// several writer paths per connection (request responses, stats events,
// docker log streams, terminal PTY output), so every writer goes through the
// mutex. Reads stay single-goroutine (one read loop per connection).
package wsconn

import (
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Conn is a gorilla connection safe for concurrent writers.
type Conn struct {
	WS *websocket.Conn
	mu sync.Mutex
}

// New wraps an existing connection.
func New(ws *websocket.Conn) *Conn { return &Conn{WS: ws} }

// WriteJSON sends a JSON text frame.
func (c *Conn) WriteJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.WS.WriteJSON(v)
}

// WriteMessage sends one frame of the given type.
func (c *Conn) WriteMessage(messageType int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.WS.WriteMessage(messageType, data)
}

// WriteControl forwards a control frame (pings etc.) with a deadline.
func (c *Conn) WriteControl(messageType int, data []byte, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.WS.WriteControl(messageType, data, deadline)
}

// NextReader forwards to the underlying connection (single reader allowed).
func (c *Conn) NextReader() (int, io.Reader, error) { return c.WS.NextReader() }

// SetReadDeadline forwards to the underlying connection.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.WS.SetReadDeadline(t) }

// Close closes the underlying connection.
func (c *Conn) Close() error { return c.WS.Close() }

// ReadJSON decodes one text frame into v.
func (c *Conn) ReadJSON(v any) error {
	_, r, err := c.NextReader()
	if err != nil {
		return err
	}
	return json.NewDecoder(r).Decode(v)
}
