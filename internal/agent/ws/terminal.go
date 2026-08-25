package ws

import (
	"encoding/json"
	"log/slog"

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
	if st.curTerm() != nil {
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
	var rec *terminal.Recorder
	if h.Term.Record {
		if r, rerr := terminal.NewRecorder(h.Term.RecordingDir, h.Term.NodeID); rerr != nil {
			slog.Warn("session recording unavailable, continuing unrecorded", "err", rerr)
		} else {
			rec = r
		}
	}
	s, err := terminal.Open(req.User, req.Password, req.Cols, req.Rows, rec)
	if err != nil {
		if rec != nil {
			rec.Close() // session never started; don't leak the .ttyrec handle
		}
		respondErr(c, e.ID, err.Error())
		return
	}
	st.setTerm(s)
	respond(c, e.ID, map[string]string{"status": "ok"})
	go pump(c, st, s)
}

// pump fans pty output into binary frames. Backpressure: the underlying
// socket write blocks the pty read (nothing is dropped while the client is
// healthy); a stalled client trips wsconn's write deadline, which ends the
// session rather than orphaning the shell.
// ponytail: blocking read->write loop instead of a buffered fan-in chan,
// add one only if the pty must keep draining while the socket stalls.
func pump(c *wsconn.Conn, st *ConnState, s *terminal.Session) {
	buf := make([]byte, 32768)
	dead := false
	for {
		n, err := s.Read(buf)
		if n > 0 {
			if c.WriteMessage(websocket.BinaryMessage, buf[:n]) != nil {
				dead = true
			}
			if dead {
				break // conn dead; the session is closed below
			}
			continue
		}
		if err != nil {
			break // pty closed or shell exited
		}
	}
	if dead {
		// Nothing will ever read this pty again, so tear the session down
		// here; serveConn's defer only runs when its read loop errors.
		s.Close()
	}
	// Shell died on its own: notify, unless a new session already took
	// over this connection.
	if st.curTerm() == s {
		st.takeTerm()
		c.WriteJSON(protocol.NewEvent(protocol.MethodTerminalExit, map[string]string{}))
	}
}

// TerminalResize resizes the active session, if any.
func (h *Handler) TerminalResize(c *wsconn.Conn, e protocol.Envelope, st *ConnState) {
	s := st.curTerm()
	if s == nil {
		respondErr(c, e.ID, "no active terminal")
		return
	}
	var req struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	json.Unmarshal(e.Payload, &req)
	if err := s.Resize(req.Cols, req.Rows); err != nil {
		respondErr(c, e.ID, err.Error())
		return
	}
	respond(c, e.ID, map[string]string{"status": "ok"})
}

// TerminalClose tears the active session down; a later terminal.open may
// start a new one on the same connection.
func (h *Handler) TerminalClose(c *wsconn.Conn, e protocol.Envelope, st *ConnState) {
	s := st.takeTerm()
	if s == nil {
		respondErr(c, e.ID, "no active terminal")
		return
	}
	s.Close()
	respond(c, e.ID, map[string]string{"status": "ok"})
}
