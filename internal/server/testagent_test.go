package server_test

// fakeAgent is an in-process agent speaking the gopit WS protocol. It backs
// multiple nodes at once (one connection per node, like a real agent would
// accept), serving stats events, terminal echo, docker log streams and
// firewall/compose responses. Docker itself is never touched: the protocol
// layer under test is identical.

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	agentws "gopit/internal/agent/ws"
	"gopit/internal/agent/wsconn"
	"gopit/internal/protocol"
)

type fakeSession struct {
	c       *wsconn.Conn
	term    bool // terminal.open accepted; binary frames echo
	streams map[string]chan struct{}
}

type fakeAgent struct {
	token  string
	URL    string // ws://host/ws
	stats  time.Duration
	closed chan struct{}

	mu    sync.Mutex
	conns map[*wsconn.Conn]*fakeSession
	// received records the last control request per method, keyed by method.
	received map[string]json.RawMessage

	logStreams atomic.Int64
	ts         *httptest.Server
}

func newFakeAgent(t *testing.T, token string, statsInterval time.Duration) *fakeAgent {
	t.Helper()
	a := &fakeAgent{
		token: token, stats: statsInterval,
		closed:   make(chan struct{}),
		conns:    map[*wsconn.Conn]*fakeSession{},
		received: map[string]json.RawMessage{},
	}
	a.ts = httptest.NewServer(http.HandlerFunc(a.serveWS))
	a.URL = "ws" + strings.TrimPrefix(a.ts.URL, "http") + "/ws"
	t.Cleanup(a.Close)
	go a.statsLoop()
	return a
}

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func (a *fakeAgent) serveWS(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	wc := wsconn.New(c)
	wc.WS.SetReadLimit(4 << 20)
	sess := &fakeSession{c: wc, streams: map[string]chan struct{}{}}
	a.mu.Lock()
	a.conns[wc] = sess
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.conns, wc)
		for _, stop := range sess.streams {
			close(stop)
		}
		a.mu.Unlock()
		c.Close()
	}()
	if !a.authLoop(wc) {
		return
	}
	a.readLoop(sess)
}

func (a *fakeAgent) authLoop(c *wsconn.Conn) bool {
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.SetReadDeadline(deadline)
		var e protocol.Envelope
		if err := c.ReadJSON(&e); err != nil {
			return false
		}
		if e.Method != "auth" {
			c.WriteJSON(protocol.NewErrorResponse(e.ID, "auth required"))
			continue
		}
		var p agentws.AuthPayload
		json.Unmarshal(e.Payload, &p)
		if p.Token != a.token {
			c.WriteJSON(protocol.NewErrorResponse(e.ID, "invalid token"))
			return false
		}
		c.WriteJSON(protocol.NewResponse(e.ID, map[string]string{"status": "ok"}))
		return true
	}
}

func (a *fakeAgent) readLoop(sess *fakeSession) {
	for {
		mt, r, err := sess.c.NextReader()
		if err != nil {
			return
		}
		if mt == websocket.BinaryMessage {
			if sess.term { // echo terminal input back
				b, err := io.ReadAll(r)
				if err == nil {
					sess.c.WriteMessage(websocket.BinaryMessage, b)
				}
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
		a.handle(sess, e)
	}
}

func (a *fakeAgent) handle(sess *fakeSession, e protocol.Envelope) {
	a.mu.Lock()
	a.received[e.Method] = e.Payload
	a.mu.Unlock()
	switch e.Method {
	case "terminal.open":
		sess.term = true
		a.respond(sess.c, e.ID, map[string]string{"status": "ok"})
	case "terminal.resize":
		a.respond(sess.c, e.ID, map[string]string{"status": "ok"})
	case "terminal.close":
		sess.term = false
		a.respond(sess.c, e.ID, map[string]string{"status": "ok"})
	case "docker.container.logs":
		var req struct {
			ContainerID string `json:"container_id"`
		}
		json.Unmarshal(e.Payload, &req)
		stop := make(chan struct{})
		a.mu.Lock()
		sess.streams[req.ContainerID] = stop
		a.mu.Unlock()
		a.logStreams.Add(1)
		a.respond(sess.c, e.ID, map[string]bool{"stream": true})
		go a.logPump(sess, req.ContainerID, stop)
	case "docker.container.logs.stop":
		var req struct {
			ContainerID string `json:"container_id"`
		}
		json.Unmarshal(e.Payload, &req)
		a.mu.Lock()
		if stop, ok := sess.streams[req.ContainerID]; ok {
			close(stop)
			delete(sess.streams, req.ContainerID)
		}
		a.mu.Unlock()
		a.respond(sess.c, e.ID, map[string]string{"status": "ok"})
	case "docker.compose.deploy", "docker.compose.validate", "docker.compose.down":
		a.respond(sess.c, e.ID, map[string]string{"status": "ok"})
	case "docker.compose.list":
		a.respond(sess.c, e.ID, []map[string]string{})
	case "docker.compose.ps":
		a.respond(sess.c, e.ID, []map[string]string{})
	case "ufw.status":
		a.respond(sess.c, e.ID, map[string]any{"enabled": false, "default_in": "allow", "default_out": "allow", "rules": []map[string]any{}})
	case "ufw.rule.preview":
		var req struct {
			Port int `json:"port"`
		}
		json.Unmarshal(e.Payload, &req)
		a.respond(sess.c, e.ID, map[string]any{"enabled": false, "default_in": "allow", "default_out": "allow",
			"rules": []map[string]any{{"number": 1, "to": fmt.Sprintf("%d/tcp", req.Port), "action": "ALLOW", "from": "Anywhere", "direction": "IN"}}})
	case "ufw.rule.add", "ufw.rule.delete", "ufw.toggle":
		a.respond(sess.c, e.ID, map[string]string{"status": "ok"})
	case "system.info":
		a.respond(sess.c, e.ID, map[string]any{"uuid": "fake", "hostname": "fake", "ip": "127.0.0.1", "port": 1221})
	case "system.stats":
		a.respond(sess.c, e.ID, sampleStats())
	default:
		sess.c.WriteJSON(protocol.NewErrorResponse(e.ID, "unknown method: "+e.Method))
	}
}

func (a *fakeAgent) respond(c *wsconn.Conn, id string, payload any) {
	if err := c.WriteJSON(protocol.NewResponse(id, payload)); err != nil {
		slog.Warn("fakeAgent write failed", "err", err)
	}
}

func (a *fakeAgent) logPump(sess *fakeSession, cid string, stop chan struct{}) {
	defer a.logStreams.Add(-1)
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-a.closed:
			return
		case <-stop:
			return
		case <-t.C:
			evt := protocol.NewEvent("docker.container.log", map[string]string{"container_id": cid, "line": "fake log line"})
			if sess.c.WriteJSON(evt) != nil {
				return
			}
		}
	}
}

func (a *fakeAgent) statsLoop() {
	t := time.NewTicker(a.stats)
	defer t.Stop()
	for {
		select {
		case <-a.closed:
			return
		case <-t.C:
			a.mu.Lock()
			conns := make([]*wsconn.Conn, 0, len(a.conns))
			for c := range a.conns {
				conns = append(conns, c)
			}
			a.mu.Unlock()
			evt := protocol.NewEvent("system.stats", sampleStats())
			for _, c := range conns {
				if c.WriteJSON(evt) != nil {
					c.Close()
				}
			}
		}
	}
}

// got reports whether the agent has received a request with the method.
func (a *fakeAgent) got(method string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.received[method]
	return ok
}

func (a *fakeAgent) streamCount() int64 { return a.logStreams.Load() }

// port returns the TCP port the agent listens on.
func (a *fakeAgent) port(t *testing.T) int {
	t.Helper()
	_, p, err := net.SplitHostPort(strings.TrimPrefix(a.ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Close stops all connections and goroutines.
func (a *fakeAgent) Close() {
	select {
	case <-a.closed:
	default:
		close(a.closed)
	}
	a.mu.Lock()
	for c := range a.conns {
		c.Close()
	}
	a.conns = map[*wsconn.Conn]*fakeSession{}
	a.mu.Unlock()
	a.ts.Close()
}

func sampleStats() protocol.SystemStats {
	return protocol.SystemStats{
		CPU:  protocol.CPUStats{Percent: 1.5, Cores: 2},
		Mem:  protocol.MemStats{Total: 1 << 30, Used: 1 << 29, Free: 1 << 29, Percent: 50},
		Disk: []protocol.DiskStats{{Mount: "/", Total: 1 << 30, Used: 1 << 29, Free: 1 << 29, Percent: 50}},
		Net:  protocol.NetStats{RxBytes: 1, TxBytes: 1},
	}
}
