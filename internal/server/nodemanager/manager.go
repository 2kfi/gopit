// Package nodemanager owns server<->agent WebSocket connections.
package nodemanager

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"gopit/internal/protocol"
	"gopit/internal/server/store"
)

// Callbacks are invoked on connection lifecycle events.
type Callbacks struct {
	OnStats  func(nodeID string, stats protocol.SystemStats)
	OnStatus func(nodeID, status string)
}

// Manager connects to approved nodes and reconnects with backoff.
type Manager struct {
	store   *store.Store
	cb      Callbacks
	skipTLS bool
	mu      sync.Mutex
	conns   map[string]*Conn // nodeID -> live connection
	looping map[string]bool  // nodeID -> a connectLoop goroutine is running
	stop    chan struct{}
}

// Conn is a live agent connection.
type Conn struct {
	WS       *websocket.Conn
	subMu    sync.Mutex
	subs     map[chan protocol.Envelope]struct{} // browser subscribers
	stop     chan struct{}
	stopOnce sync.Once
}

// New creates the manager.
func New(s *store.Store, cb Callbacks, tlsSkipVerify bool) *Manager {
	return &Manager{
		store:   s,
		cb:      cb,
		skipTLS: tlsSkipVerify,
		conns:   make(map[string]*Conn),
		looping: make(map[string]bool),
		stop:    make(chan struct{}),
	}
}

// Subscribe registers a channel to receive this node's events.
func (c *Conn) Subscribe(ch chan protocol.Envelope) {
	c.subMu.Lock()
	c.subs[ch] = struct{}{}
	c.subMu.Unlock()
}

// Unsubscribe removes a subscriber channel.
func (c *Conn) Unsubscribe(ch chan protocol.Envelope) {
	c.subMu.Lock()
	delete(c.subs, ch)
	c.subMu.Unlock()
}

// broadcast fans an envelope out to all browser subscribers.
func (c *Conn) broadcast(e protocol.Envelope) {
	c.subMu.Lock()
	for ch := range c.subs {
		select {
		case ch <- e:
		default: // drop when a browser subscriber is slow
		}
	}
	c.subMu.Unlock()
}

// Close stops all connections and goroutines.
func (m *Manager) Close() {
	close(m.stop)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conns {
		c.WS.Close()
	}
	m.conns = map[string]*Conn{}
	m.looping = map[string]bool{}
}

// Connect dials a node. Returns existing connection if already live.
func (m *Manager) Connect(n *store.Node) (*Conn, error) {
	m.mu.Lock()
	if c, ok := m.conns[n.ID]; ok {
		m.mu.Unlock()
		return c, nil
	}
	m.mu.Unlock()

	ws, err := Dial(n, m.skipTLS)
	if err != nil {
		return nil, err
	}
	conn := &Conn{WS: ws, subs: make(map[chan protocol.Envelope]struct{}), stop: make(chan struct{})}
	m.mu.Lock()
	if old, ok := m.conns[n.ID]; ok { // raced with another dial
		m.mu.Unlock()
		ws.Close()
		return old, nil
	}
	m.conns[n.ID] = conn
	m.mu.Unlock()

	m.cb.OnStatus(n.ID, store.StatusOnline)
	go m.readLoop(n.ID, conn)
	go m.pingLoop(conn)
	return conn, nil
}

// pingLoop keeps the connection alive; a failed write kills the conn and
// triggers reconnect.
func (m *Manager) pingLoop(c *Conn) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-c.stop:
			return
		case <-t.C:
			if err := c.WS.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				c.WS.Close()
				return
			}
		}
	}
}

func (m *Manager) readLoop(nodeID string, conn *Conn) {
	defer func() {
		ws := conn.WS
		ws.Close()
		m.mu.Lock()
		if m.conns[nodeID] == conn {
			delete(m.conns, nodeID)
		}
		m.mu.Unlock()
		m.cb.OnStatus(nodeID, store.StatusOffline)
		close(conn.stop)
	}()
	for {
		var e protocol.Envelope
		if err := wsReadJSON(conn.WS, &e); err != nil {
			return
		}
		switch e.Type {
		case protocol.TypeEvent:
			if e.Method == "system.stats" {
				var stats protocol.SystemStats
				if err := json.Unmarshal(e.Payload, &stats); err == nil {
					m.cb.OnStats(nodeID, stats)
				}
			}
			conn.broadcast(e) // system.stats, docker.container.log, ... every subscriber decides
		case protocol.TypeResponse:
			conn.broadcast(e)
		}
	}
}

// Reconcile connects to every connectable node (approved or offline: offline
// merely means "approved but not currently connected") and schedules
// reconnects.
func (m *Manager) Reconcile() {
	nodes, err := m.store.ListNodes()
	if err != nil {
		slog.Error("reconcile list failed", "err", err)
		return
	}
	for i := range nodes {
		n := nodes[i]
		if n.Status != store.StatusApproved && n.Status != store.StatusOffline {
			continue
		}
		m.mu.Lock()
		_, live := m.conns[n.ID]
		if !live && !m.looping[n.ID] {
			m.looping[n.ID] = true
			go m.connectLoop(n)
		}
		m.mu.Unlock()
	}
}

// connectLoop keeps trying with exponential backoff until the node is gone or
// the manager stops. A dropped established connection also re-enters the
// loop, with the backoff reset.
func (m *Manager) connectLoop(n store.Node) {
	defer func() {
		m.mu.Lock()
		delete(m.looping, n.ID)
		m.mu.Unlock()
	}()
	backoff := 2 * time.Second
	max := 60 * time.Second
	for {
		select {
		case <-m.stop:
			return
		default:
		}
		if _, err := m.store.GetNode(n.ID); err != nil {
			return // node deleted
		}
		conn, err := m.Connect(&n)
		if err != nil {
			slog.Warn("node connect failed", "id", n.ID, "ip", n.IP, "err", err)
			select {
			case <-m.stop:
				return
			case <-time.After(backoff):
			}
			if backoff < max {
				backoff *= 2
			}
			continue
		}
		<-conn.stop
		backoff = 2 * time.Second
	}
}

// Conn returns the live connection for nodeID, or nil.
func (m *Manager) Conn(nodeID string) *Conn {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conns[nodeID]
}

// Request sends a request and waits for the matching response.
func (c *Conn) Request(method string, payload any, timeout time.Duration) (protocol.Envelope, error) {
	e := protocol.NewRequest(method, payload)
	if err := c.WS.WriteJSON(e); err != nil {
		return protocol.Envelope{}, err
	}
	ch := make(chan protocol.Envelope, 8)
	c.Subscribe(ch)
	defer c.Unsubscribe(ch)
	resp, ok := protocol.WaitForID(ch, e.ID, timeout)
	if !ok {
		return protocol.Envelope{}, &protocol.ErrRemote{Msg: "request timed out"}
	}
	return resp, nil
}

func wsReadJSON(ws *websocket.Conn, out any) error {
	_, r, err := ws.NextReader()
	if err != nil {
		return err
	}
	return json.NewDecoder(r).Decode(out)
}
