package api

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"gopit/internal/server/metrics"
)

var (
	wsMu  sync.Mutex
	wsSet = map[*websocket.Conn]struct{}{}
)

// TrackWS registers a browser WebSocket for the drain + metrics gauge. Call
// the returned release func when the connection closes (e.g. `defer
// TrackWS(ws)()` right after a successful upgrade). WriteControl is safe to
// call from DrainWS concurrently with handler reads/writes.
func TrackWS(c *websocket.Conn) func() {
	wsMu.Lock()
	wsSet[c] = struct{}{}
	wsMu.Unlock()
	metrics.WSConnections.Inc()
	var once sync.Once
	return func() {
		once.Do(func() {
			wsMu.Lock()
			delete(wsSet, c)
			wsMu.Unlock()
			metrics.WSConnections.Dec()
		})
	}
}

// DrainWS sends a close frame to every tracked browser connection and waits
// (bounded by ctx) for the handlers to release them. The handlers observe the
// peer close and tear down; a dead browser is cut by the ctx deadline.
func DrainWS(ctx context.Context) {
	wsMu.Lock()
	conns := make([]*websocket.Conn, 0, len(wsSet))
	for c := range wsSet {
		conns = append(conns, c)
	}
	wsMu.Unlock()
	for _, c := range conns {
		c.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"),
			time.Now().Add(2*time.Second))
	}
	for {
		wsMu.Lock()
		n := len(wsSet)
		wsMu.Unlock()
		if n == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}
