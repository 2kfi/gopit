package ws

import (
	"encoding/json"
	"time"

	"github.com/gorilla/websocket"

	"gopit/internal/agent/terminal"
	"gopit/internal/agent/wsconn"
	"gopit/internal/protocol"
)

// TerminalOpen validates credentials and opens a session on this connection.
// Once open, binary frames on this connection are raw terminal input and the
// session's pty output flows back as binary frames; resize/close stay JSON
// envelopes. The connection is closed by the client to end the session, or
// the pty pump emits terminal.exit when the shell dies on its own.
func (h *Handler) TerminalOpen(c *wsconn.Conn, e protocol.Envelope, st *ConnState) {
	if st.term != nil {
		respondErr(c, e.ID, "a terminal is already open on this connection")
		return
	}
	var req struct {
		User     string `json:"user"`
		Password string `json:"password"`
		Cols     int    `json:"cols"`
		Rows     int    `json:"rows"`
	}
	if err := json.Unmarshal(e.Payload, &req); err != nil || req.User == "" {
		respondErr(c, e.ID, "user required")
		return
	}
	// Password is never logged; it only ever reaches the validation pty.
	s, err := terminal.Open(req.User, req.Password, req.Cols, req.Rows)
	if err != nil {
		respondErr(c, e.ID, err.Error())
		return
	}
	st.term = s
	respond(c, e.ID, map[string]string{"status": "ok"})
	go pump(c, st, s)
}

// pump fans pty output into binary frames. Backpressure: the underlying
// socket write blocks the pty read (nothing is dropped while the client is
// healthy); a stalled client drops chunks after 1s (bounded memory).
// ponytail: blocking read->write loop instead of a buffered fan-in chan,
// add one only if the pty must keep draining while the socket stalls.
func pump(c *wsconn.Conn, st *ConnState, s *terminal.Session) {
	buf := make([]byte, 32768)
	for {
		n, err := s.Read(buf)
		if n > 0 {
			select {
			case <-time.After(time.Second):
				// client stalled; drop rather than block the pty forever
			default:
				if c.WriteMessage(websocket.BinaryMessage, buf[:n]) != nil {
					break // conn dead; serveConn's defer tears the session down
				}
			}
			continue
		}
		if err != nil {
			break // pty closed or shell exited
		}
	}
	// Shell died or session was closed: notify, unless a new session already
	// took over this connection.
	if st.term == s {
		st.term = nil
		c.WriteJSON(protocol.NewEvent(MethodTerminalExit, map[string]string{}))
	}
}

// TerminalResize resizes the active session, if any.
func (h *Handler) TerminalResize(c *wsconn.Conn, e protocol.Envelope, st *ConnState) {
	if st.term == nil {
		respondErr(c, e.ID, "no active terminal")
		return
	}
	var req struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	json.Unmarshal(e.Payload, &req)
	if err := st.term.Resize(req.Cols, req.Rows); err != nil {
		respondErr(c, e.ID, err.Error())
		return
	}
	respond(c, e.ID, map[string]string{"status": "ok"})
}

// TerminalClose tears the active session down; a later terminal.open may
// start a new one on the same connection.
func (h *Handler) TerminalClose(c *wsconn.Conn, e protocol.Envelope, st *ConnState) {
	if st.term == nil {
		respondErr(c, e.ID, "no active terminal")
		return
	}
	st.term.Close()
	st.term = nil
	respond(c, e.ID, map[string]string{"status": "ok"})
}
