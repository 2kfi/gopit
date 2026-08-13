package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	agentws "gopit/internal/agent/ws"
	"gopit/internal/protocol"
	"gopit/internal/server/nodemanager"
	"gopit/internal/server/store"
)

// TerminalAPI bridges a browser WebSocket to a per-session agent connection.
// Browser frames: first message is a text frame {user, password}; after the
// handshake, binary frames are raw terminal input and text frames are JSON
// control ({type:"resize"|"close"}). Agent frames: binary = pty output,
// text = protocol envelopes (terminal.exit events are relayed as
// {type:"exit"}).
type TerminalAPI struct {
	store    *store.Store
	skipTLS  bool
	upgrader websocket.Upgrader
}

// NewTerminalAPI wires the terminal route.
func NewTerminalAPI(s *store.Store, skipTLS bool) *TerminalAPI {
	return &TerminalAPI{
		store:   s,
		skipTLS: skipTLS,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true }, // same-origin JWT cookie already checked
		},
	}
}

// Stream upgrades the browser connection, forwards {user,password} to the
// agent as terminal.open, then relays bytes in both directions until either
// side closes. The password exists only in this function's memory; it is
// never logged.
func (a *TerminalAPI) Stream(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "uuid")
	node, err := a.store.GetNode(nodeID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "node not found")
		return
	}
	ws, err := a.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	mt, data, err := ws.ReadMessage()
	ws.SetReadDeadline(time.Time{})
	if err != nil || mt != websocket.TextMessage {
		return
	}
	var creds struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(data, &creds); err != nil || creds.User == "" {
		terminalMsg(ws, "missing credentials")
		return
	}

	aw, err := nodemanager.Dial(node, a.skipTLS) // per-session agent conn: binary frames need no session id
	if err != nil {
		terminalMsg(ws, "agent unreachable: "+err.Error())
		return
	}
	defer aw.Close()

	openReq := protocol.NewRequest(agentws.MethodTerminalOpen, map[string]any{
		"user":     creds.User,
		"password": creds.Password,
		"cols":     80,
		"rows":     24,
	})
	if err := aw.WriteJSON(openReq); err != nil {
		terminalMsg(ws, "agent connection failed")
		return
	}
	aw.SetReadDeadline(time.Now().Add(15 * time.Second))
	var resp protocol.Envelope
	for {
		if err := aw.ReadJSON(&resp); err != nil {
			terminalMsg(ws, "no agent response")
			return
		}
		if resp.ID == openReq.ID {
			break
		}
	}
	aw.SetReadDeadline(time.Time{})
	if resp.Error != nil && *resp.Error != "" {
		terminalMsg(ws, *resp.Error) // e.g. "root is not allowed", "authentication failed"
		return
	}
	if err := terminalMsg(ws, ""); err != nil { // handshake ack ({"type":"open"})
		return
	}

	browserDone := make(chan struct{})
	agentDone := make(chan struct{})
	// browser -> agent
	go func() {
		defer close(browserDone)
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				break
			}
			if mt == websocket.BinaryMessage {
				if err := aw.WriteMessage(websocket.BinaryMessage, data); err != nil {
					break
				}
				continue
			}
			var ctrl struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			json.Unmarshal(data, &ctrl)
			switch ctrl.Type {
			case "resize":
				aw.WriteJSON(protocol.NewRequest(agentws.MethodTerminalResize, map[string]int{"cols": ctrl.Cols, "rows": ctrl.Rows}))
			case "close":
				aw.WriteJSON(protocol.NewRequest(agentws.MethodTerminalClose, nil))
				return
			}
		}
		// browser vanished: end the session on the agent side
		aw.WriteJSON(protocol.NewRequest(agentws.MethodTerminalClose, nil))
	}()
	// agent -> browser
	go func() {
		defer close(agentDone)
		for {
			mt, r, err := aw.NextReader()
			if err != nil {
				break
			}
			if mt == websocket.BinaryMessage {
				b, err := io.ReadAll(r)
				if err != nil || ws.WriteMessage(websocket.BinaryMessage, b) != nil {
					break
				}
				continue
			}
			var e protocol.Envelope
			if err := json.NewDecoder(r).Decode(&e); err != nil {
				continue
			}
			if e.Type == protocol.TypeEvent && e.Method == agentws.MethodTerminalExit {
				ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"exit"}`))
				break
			}
			// other envelopes (resize acks etc.) are internal; drop
		}
	}()

	// tear everything down when either direction dies
	go func() {
		<-browserDone
		aw.Close()
	}()
	go func() {
		<-agentDone
		ws.Close()
	}()
	<-browserDone
	<-agentDone
}

// terminalMsg sends the post-open handshake ack or a pre-open error to the
// browser. msg=="" ack with {type:"open"}.
func terminalMsg(ws *websocket.Conn, msg string) error {
	p := map[string]string{"type": "open"}
	if msg != "" {
		p = map[string]string{"type": "error", "message": msg}
	}
	b, _ := json.Marshal(p)
	return ws.WriteMessage(websocket.TextMessage, b)
}
