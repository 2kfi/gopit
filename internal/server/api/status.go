package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"gopit/internal/protocol"
	"gopit/internal/server/nodemanager"
	"gopit/internal/server/store"
)

// StatusAPI relays a node's live event stream to the browser over WS.
type StatusAPI struct {
	manager  *nodemanager.Manager
	store    *store.Store
	upgrader websocket.Upgrader
}

// NewStatusAPI wires the status stream route.
func NewStatusAPI(m *nodemanager.Manager, s *store.Store) *StatusAPI {
	return &StatusAPI{
		manager: m,
		store:   s,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true }, // same-origin JWT cookie already checked
		},
	}
}

// Stream upgrades to WS and relays the node's system.stats events as JSON text frames.
func (a *StatusAPI) Stream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "uuid")
	conn := a.manager.Conn(id)
	if conn == nil {
		writeErr(w, http.StatusNotFound, "node not connected")
		return
	}
	ws, err := a.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer TrackWS(ws)()
	ws.SetReadLimit(4 << 20) // bounded browser frames, matches the agent-side limit
	sub := make(chan protocol.Envelope, 128)
	conn.Subscribe(sub)
	defer conn.Unsubscribe(sub)

	ping := make(chan struct{})
	go func() {
		for {
			// liveness: the browser pings every 25s; silence past 60s means
			// a half-open conn — stop relaying (and free the subscription).
			ws.SetReadDeadline(time.Now().Add(60 * time.Second))
			if _, _, err := ws.ReadMessage(); err != nil {
				close(ping)
				return
			}
		}
	}()
	for {
		select {
		case e, ok := <-sub:
			if !ok {
				ws.Close()
				return
			}
			// only the stats broadcast belongs here; responses/events of
			// other sessions (docker inspect, terminal exit) must not leak.
			if e.Method != "system.stats" {
				continue
			}
			if err := ws.WriteJSON(e); err != nil {
				ws.Close()
				return
			}
		case <-ping:
			ws.Close()
			return
		}
	}
}
