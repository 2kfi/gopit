package ws

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"gopit/internal/agent/terminal"
	"gopit/internal/agent/wsconn"
	"gopit/internal/protocol"
)

// Server is the agent's WebSocket endpoint. It accepts multiple concurrent
// connections: one control connection from the gopit server (stats, docker)
// plus one per active terminal session. All connections authenticate with the
// node token first.
type Server struct {
	handler         *Handler
	token           string
	statsInterval   time.Duration
	tlsCert, tlsKey string
	upgrader        websocket.Upgrader
	mu              sync.Mutex
	conns           map[*wsconn.Conn]struct{}
	control         *wsconn.Conn // stats events go to this conn (first registered)
	closed          chan struct{}
}

// ConnState is per-connection state owned by the read loop.
type ConnState struct {
	term *terminal.Session // non-nil while a terminal session is open on this conn
}

// NewServer creates the WS server; call Start to run it.
func NewServer(token string, interval time.Duration, h *Handler) *Server {
	return &Server{
		handler:       h,
		token:         token,
		statsInterval: interval,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true }, // token-gated; no browser origin to check
		},
		conns:  make(map[*wsconn.Conn]struct{}),
		closed: make(chan struct{}),
	}
}

// SetTLS enables TLS serving when both cert and key paths are non-empty.
func (s *Server) SetTLS(cert, key string) { s.tlsCert, s.tlsKey = cert, key }

// Start begins the stats event loop and blocks serving HTTP until Close.
func (s *Server) Start(addr string) error {
	go s.statsLoop()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.serveWS)
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-s.closed
		srv.Close()
	}()
	if s.tlsCert != "" {
		return srv.ListenAndServeTLS(s.tlsCert, s.tlsKey)
	}
	return srv.ListenAndServe()
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	c, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	wc := wsconn.New(c)
	s.mu.Lock()
	s.conns[wc] = struct{}{}
	if s.control == nil { // ponytail: first conn wins; stats pause until the next (re)connect if it was a terminal conn
		s.control = wc
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, wc)
		if s.control == wc {
			s.control = nil
		}
		s.mu.Unlock()
		c.Close()
	}()
	s.serveConn(wc)
}

// statsLoop pushes system.stats events to the control connection.
func (s *Server) statsLoop() {
	if s.handler.OnStats == nil {
		return
	}
	t := time.NewTicker(s.statsInterval)
	defer t.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-t.C:
			stats := s.handler.OnStats()
			s.mu.Lock()
			c := s.control
			s.mu.Unlock()
			if c != nil {
				if err := c.WriteJSON(protocol.NewEvent(MethodSystemStats, stats)); err != nil {
					c.WS.Close() // let the read loop discover the dead conn
				}
			}
		}
	}
}

// serveConn authenticates the connection, then runs the request loop.
// Binary frames are raw terminal input for the active session (if any);
// text frames are JSON envelopes (control plane).
func (s *Server) serveConn(c *wsconn.Conn) {
	st := &ConnState{}
	done := make(chan struct{}) // closes when the connection dies; streaming handlers cancel on it
	defer func() {
		close(done)
		if st.term != nil {
			st.term.Close() // SIGHUP the session's process group, reap via its reaper goroutine
		}
	}()
	authTimeout := time.Now().Add(10 * time.Second)
	for {
		c.SetReadDeadline(authTimeout)
		var e protocol.Envelope
		if err := c.ReadJSON(&e); err != nil {
			return
		}
		if e.Type != protocol.TypeRequest || e.Method != MethodAuth {
			c.WriteJSON(protocol.NewErrorResponse(e.ID, "auth required"))
			continue
		}
		var p AuthPayload
		json.Unmarshal(e.Payload, &p)
		if subtle.ConstantTimeCompare([]byte(p.Token), []byte(s.token)) != 1 {
			c.WriteJSON(protocol.NewErrorResponse(e.ID, "invalid token"))
			time.AfterFunc(100*time.Millisecond, func() { c.Close() })
			return
		}
		c.WriteJSON(protocol.NewResponse(e.ID, map[string]string{"status": "ok"}))
		break
	}
	c.SetReadDeadline(time.Time{})
	for {
		mt, r, err := c.NextReader()
		if err != nil {
			return
		}
		if mt == websocket.BinaryMessage {
			if st.term == nil {
				continue // stray data frame without a session: drop
			}
			b, err := io.ReadAll(r)
			if err != nil {
				return
			}
			if len(b) > 0 { // empty-input guard: a 0-byte frame must not hit the pty
				st.term.Write(b)
			}
			continue
		}
		var e protocol.Envelope
		if err := json.NewDecoder(r).Decode(&e); err != nil {
			continue
		}
		if e.Type != protocol.TypeRequest {
			continue
		}
		s.handler.Handle(c, e, done, st)
	}
}

// Close shuts the server down.
func (s *Server) Close() {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	s.mu.Lock()
	for c := range s.conns {
		c.Close()
	}
	s.conns = map[*wsconn.Conn]struct{}{}
	s.control = nil
	s.mu.Unlock()
}
