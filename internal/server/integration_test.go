// Package server_test exercises the full control plane: real HTTP server
// (auth + CSRF + rate limiting + proxies), a real agent WS server + UDP
// beacon (discovery, connect, stats stream) and fake protocol agents for the
// paths a real agent cannot run in CI (terminal pty, docker, firewall).
package server_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"

	agentdisc "gopit/internal/agent/discovery"
	agentws "gopit/internal/agent/ws"
	"gopit/internal/protocol"
	"gopit/internal/server/api"
	"gopit/internal/server/discovery"
	"gopit/internal/server/nodemanager"
	"gopit/internal/server/store"
	"gopit/internal/server/webhooks"
)

// goPit is the in-test server control plane.
type goPit struct {
	s      *store.Store
	m      *nodemanager.Manager
	ts     *httptest.Server
	cookie string
	csrf   string
	once   sync.Once
}

func newGoPit(t *testing.T, disc *discovery.Client) *goPit {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	m := nodemanager.New(s, nodemanager.Callbacks{
		OnStatus: func(id, status string) { s.SetNodeStatus(id, status) },
		OnStats:  func(string, protocol.SystemStats) {},
	}, true)
	if disc == nil {
		disc = &discovery.Client{BroadcastAddr: "127.0.0.1", Port: 1221, Timeout: 50 * time.Millisecond}
	}
	ts := httptest.NewServer(api.Router(s, m, disc, "test-secret", "", false, false, 1000, 0, webhooks.New(nil)))
	g := &goPit{s: s, m: m, ts: ts}
	t.Cleanup(g.Close)
	return g
}

func (g *goPit) Close() {
	g.once.Do(func() {
		g.m.Close()
		g.s.Close()
		g.ts.Close()
	})
}

// login as admin, priming the store with a bcrypt'd adminpass like server
// bootstrap does.
func (g *goPit) login(t *testing.T) {
	t.Helper()
	if g.cookie != "" {
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("adminpass"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.s.CreateUser("admin", string(hash)); err != nil && !strings.Contains(err.Error(), "UNIQUE constraint") {
		t.Fatal(err)
	}
	resp := g.post(t, "/api/login", map[string]string{"username": "admin", "password": "adminpass"}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "gopit_token" {
			g.cookie = c.Value
		}
	}
	var out struct {
		CSRF string `json:"csrf_token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	g.csrf = out.CSRF
}

// do performs one request with the auth cookie and (for non-GET) Origin +
// CSRF header, mimicking the browser.
func (g *goPit) do(t *testing.T, method, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rdr *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, g.ts.URL+path, rdr)
	req.Header.Set("X-Forwarded-For", "10.200.0.1")
	if g.cookie != "" {
		req.AddCookie(&http.Cookie{Name: "gopit_token", Value: g.cookie})
	}
	if method != "GET" {
		req.Header.Set("Origin", g.ts.URL)
		req.Header.Set("X-CSRF-Token", g.csrf)
	}
	rr := httptest.NewRecorder()
	g.ts.Config.Handler.ServeHTTP(rr, req)
	out := map[string]any{}
	json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Result(), out
}

func (g *goPit) get(t *testing.T, path string) (*http.Response, map[string]any) {
	return g.do(t, "GET", path, nil)
}

func (g *goPit) post(t *testing.T, path string, body any, hdr map[string]string) *http.Response {
	var rdr *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest("POST", g.ts.URL+path, rdr)
	req.Header.Set("X-Forwarded-For", "10.200.0.1")
	if hdr != nil {
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
	}
	rr := httptest.NewRecorder()
	g.ts.Config.Handler.ServeHTTP(rr, req)
	return rr.Result()
}

// ws dials a browser WebSocket endpoint carrying the auth cookie.
func (g *goPit) ws(t *testing.T, path string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(g.ts.URL, "http") + path
	hd := http.Header{}
	if g.cookie != "" {
		hd.Set("Cookie", "gopit_token="+g.cookie)
	}
	c, _, err := websocket.DefaultDialer.Dial(u, hd)
	if err != nil {
		t.Fatalf("ws %s: %v", path, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// listNodes GETs /api/nodes (a JSON array) with the auth cookie.
func (g *goPit) listNodes(t *testing.T) []map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", g.ts.URL+"/api/nodes", nil)
	req.Header.Set("X-Forwarded-For", "10.200.0.1")
	if g.cookie != "" {
		req.AddCookie(&http.Cookie{Name: "gopit_token", Value: g.cookie})
	}
	rr := httptest.NewRecorder()
	g.ts.Config.Handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("list nodes: %d", rr.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("list nodes decode: %v", err)
	}
	return out
}

// nodeStatus polls the node list until id reports the wanted status.
func (g *goPit) waitNode(t *testing.T, id, want string, d time.Duration) {
	t.Helper()
	waitFor(t, "node "+id+" "+want, func() bool {
		for _, n := range g.listNodes(t) {
			if n["id"] == id && n["status"] == want {
				return true
			}
		}
		return false
	}, d)
}

// addNode registers a node via the API (ip/port/token), approves it and waits
// until the manager reports it online; returns the created node id.
func (g *goPit) addNode(t *testing.T, ip string, port int, token string) string {
	t.Helper()
	resp, out := g.do(t, "POST", "/api/nodes", map[string]any{
		"ip": ip, "port": port, "hostname": "fake-node", "token": token,
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create node: %d %v", resp.StatusCode, out)
	}
	id, _ := out["id"].(string)
	if id == "" {
		t.Fatalf("create node returned no id: %v", out)
	}
	resp, out = g.do(t, "POST", "/api/nodes/"+id+"/approve", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("approve node: %d %v", resp.StatusCode, out)
	}
	g.waitNode(t, id, "online", 10*time.Second)
	return id
}

func waitFor(t *testing.T, what string, cond func() bool, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitTCP(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("port %d never came up", port)
}

// readStats waits for one system.stats event on a status stream.
func readStats(t *testing.T, ws *websocket.Conn, d time.Duration) protocol.SystemStats {
	t.Helper()
	ws.SetReadDeadline(time.Now().Add(d))
	for {
		var e protocol.Envelope
		if err := ws.ReadJSON(&e); err != nil {
			t.Fatalf("read stats event: %v", err)
		}
		if e.Method == "system.stats" {
			var st protocol.SystemStats
			if err := json.Unmarshal(e.Payload, &st); err != nil {
				t.Fatalf("bad stats payload: %v", err)
			}
			return st
		}
	}
}

// TestDiscoveryApproveConnectAndStats spins up the REAL agent (ws server +
// UDP beacon on one port) and drives discover -> set token -> approve ->
// connect -> live stats stream through the API, plus the terminal error path
// and the graceful docker-unavailable degradation.
func TestDiscoveryApproveConnectAndStats(t *testing.T) {
	if testing.Short() {
		t.Skip("real agent integration")
	}
	port := freePort(t)
	info := protocol.NodeInfo{UUID: "real-1", Hostname: "box1", IP: "127.0.0.1", Port: port, OS: "linux", Arch: "amd64", AgentVersion: "test"}
	beacon := agentdisc.NewBeacon(func() protocol.NodeInfo { return info })
	if err := beacon.Start(port); err != nil {
		t.Fatal(err)
	}
	defer beacon.Close()
	h := &agentws.Handler{
		OnStats: func() protocol.SystemStats { return sampleStats() },
		OnInfo:  func() protocol.NodeInfo { return info },
	}
	agentSrv := agentws.NewServer("agent-tok", 100*time.Millisecond, h)
	go agentSrv.Start("127.0.0.1:" + strconv.Itoa(port))
	defer agentSrv.Close()
	waitTCP(t, port)

	g := newGoPit(t, &discovery.Client{BroadcastAddr: "127.0.0.1", Port: port, Timeout: 1 * time.Second})
	g.login(t)

	// discovery picks up the beacon's announce as a pending node
	resp, out := g.do(t, "POST", "/api/nodes/discover", nil)
	if resp.StatusCode != 200 || out["found"].(float64) != 1 {
		t.Fatalf("discover: %d %v", resp.StatusCode, out)
	}
	resp, out = g.do(t, "POST", "/api/nodes/real-1/token", map[string]string{"token": "agent-tok"})
	if resp.StatusCode != 200 {
		t.Fatalf("set token: %d %v", resp.StatusCode, out)
	}
	resp, out = g.do(t, "POST", "/api/nodes/real-1/approve", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("approve: %d %v", resp.StatusCode, out)
	}
	g.waitNode(t, "real-1", "online", 10*time.Second)

	// health reflects the live node
	_, out = g.get(t, "/api/health")
	if out["nodes_online"].(float64) != 1 {
		t.Fatalf("health online: %v", out)
	}

	// live stats stream: the agent pushes system.stats every 100ms
	ws := g.ws(t, "/api/nodes/real-1/status")
	if st := readStats(t, ws, 3*time.Second); st.CPU.Cores == 0 {
		t.Fatalf("stats: %+v", st)
	}

	// docker is unavailable in the agent: the proxy degrades to a clean 502
	resp, out = g.do(t, "GET", "/api/nodes/real-1/containers", nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("docker list without docker: expected 502, got %d %v", resp.StatusCode, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "unknown method") {
		t.Fatalf("docker degradation message: %q", msg)
	}
}

// TestTerminalErrorPathViaRealAgent drives the full terminal proxy against
// the real agent. The pty rejects the wrong password; the browser must
// receive the typed error message (validation happens inside the agent).
func TestTerminalErrorPathViaRealAgent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("agent rejects root-run password validation")
	}
	port := freePort(t)
	info := protocol.NodeInfo{UUID: "term-1", Hostname: "box1", IP: "127.0.0.1", Port: port, OS: "linux", Arch: "amd64"}
	beacon := agentdisc.NewBeacon(func() protocol.NodeInfo { return info })
	if err := beacon.Start(port); err != nil {
		t.Fatal(err)
	}
	defer beacon.Close()
	agentSrv := agentws.NewServer("agent-tok", time.Second, &agentws.Handler{
		OnStats: func() protocol.SystemStats { return sampleStats() },
		OnInfo:  func() protocol.NodeInfo { return info },
	})
	go agentSrv.Start("127.0.0.1:" + strconv.Itoa(port))
	defer agentSrv.Close()
	waitTCP(t, port)

	g := newGoPit(t, &discovery.Client{BroadcastAddr: "127.0.0.1", Port: port, Timeout: 500 * time.Millisecond})
	g.login(t)
	g.do(t, "POST", "/api/nodes/discover", nil)
	g.do(t, "POST", "/api/nodes/term-1/token", map[string]string{"token": "agent-tok"})
	g.do(t, "POST", "/api/nodes/term-1/approve", nil)
	g.waitNode(t, "term-1", "online", 10*time.Second)

	ws := g.ws(t, "/api/nodes/term-1/terminal")
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := ws.WriteJSON(map[string]string{"user": os.Getenv("USER"), "password": "definitely-wrong"}); err != nil {
		t.Fatal(err)
	}
	var ack struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := ws.ReadJSON(&ack); err != nil {
		t.Fatalf("terminal error ack: %v", err)
	}
	if ack.Type != "error" || ack.Message == "" {
		t.Fatalf("expected typed terminal error, got %+v", ack)
	}
}

// TestTerminalProxyRoundTrip proxies a browser terminal through the server to
// a fake agent: open handshake, small and large binary frames, resize, close.
func TestTerminalProxyRoundTrip(t *testing.T) {
	agent := newFakeAgent(t, "tok", time.Hour)
	g := newGoPit(t, nil)
	g.login(t)
	node1 := g.addNode(t, "127.0.0.1", agent.port(t), "tok")

	ws := g.ws(t, "/api/nodes/"+node1+"/terminal")
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := ws.WriteJSON(map[string]string{"user": "alice", "password": "pw"}); err != nil {
		t.Fatal(err)
	}
	var ack map[string]string
	if err := ws.ReadJSON(&ack); err != nil {
		t.Fatal(err)
	}
	if ack["type"] != "open" {
		t.Fatalf("terminal ack: %v", ack)
	}

	for _, size := range []int{64, 2 << 20} {
		payload := make([]byte, size)
		payload[0] = 'x'
		if err := ws.WriteMessage(websocket.BinaryMessage, payload); err != nil {
			t.Fatal(err)
		}
		mt, back, err := ws.ReadMessage()
		if err != nil || mt != websocket.BinaryMessage || len(back) != size {
			t.Fatalf("echo %d bytes: mt=%v len=%d err=%v", size, mt, len(back), err)
		}
	}
	// resize is relayed as a control frame
	if err := ws.WriteJSON(map[string]any{"type": "resize", "cols": 100, "rows": 40}); err != nil {
		t.Fatal(err)
	}
	// close ends the session on the agent side
	if err := ws.WriteJSON(map[string]string{"type": "close"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "terminal close relayed", func() bool {
		return agent.got("terminal.close")
	}, 5*time.Second)
	// the server tears the browser socket down after the agent session ends
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("browser socket must close after terminal close")
	}
}

// TestTerminalOversizedFrameIsDropped verifies the 4MB browser frame limit
// kills the session instead of buffering unbounded input.
func TestTerminalOversizedFrameIsDropped(t *testing.T) {
	agent := newFakeAgent(t, "tok", time.Hour)
	g := newGoPit(t, nil)
	g.login(t)
	node1 := g.addNode(t, "127.0.0.1", agent.port(t), "tok")

	ws := g.ws(t, "/api/nodes/"+node1+"/terminal")
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	ws.WriteJSON(map[string]string{"user": "alice", "password": "pw"})
	var ack map[string]string
	ws.ReadJSON(&ack)
	if ack["type"] != "open" {
		t.Fatalf("terminal ack: %v", ack)
	}
	if err := ws.WriteMessage(websocket.BinaryMessage, make([]byte, 8<<20)); err == nil {
		// the server stops reading; the write may still land in the kernel buffer
		ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, _, err := ws.ReadMessage(); err == nil {
			t.Fatal("connection must die on an oversized frame")
		}
	}
}

// TestDockerLogsStreamAndDisconnectCleanup streams container logs from a fake
// agent to a browser WS and verifies the agent stream is cancelled when the
// browser goes away.
func TestDockerLogsStreamAndDisconnectCleanup(t *testing.T) {
	agent := newFakeAgent(t, "tok", time.Hour)
	g := newGoPit(t, nil)
	g.login(t)
	node1 := g.addNode(t, "127.0.0.1", agent.port(t), "tok")

	ws := g.ws(t, "/api/nodes/"+node1+"/containers/c1/logs")
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	gotLine := false
	for !gotLine {
		var e protocol.Envelope
		if err := ws.ReadJSON(&e); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(e.Method, "docker.container.log") {
			var p struct {
				ContainerID string `json:"container_id"`
				Line        string `json:"line"`
			}
			json.Unmarshal(e.Payload, &p)
			if p.ContainerID == "c1" && p.Line != "" {
				gotLine = true
			}
		}
	}
	if agent.streamCount() != 1 {
		t.Fatalf("expected 1 agent stream, got %d", agent.streamCount())
	}
	ws.Close()
	waitFor(t, "logs.stop relayed", func() bool { return agent.got("docker.container.logs.stop") }, 5*time.Second)
	waitFor(t, "agent stream cancelled", func() bool { return agent.streamCount() == 0 }, 5*time.Second)
}

// TestComposeDeployAndFirewall drives compose deploy and firewall add/delete
// through the server proxies against a fake agent.
func TestComposeDeployAndFirewall(t *testing.T) {
	agent := newFakeAgent(t, "tok", time.Hour)
	g := newGoPit(t, nil)
	g.login(t)
	node1 := g.addNode(t, "127.0.0.1", agent.port(t), "tok")

	// firewall: add and delete
	resp, out := g.do(t, "POST", "/api/nodes/"+node1+"/firewall/rules",
		map[string]any{"protocol": "tcp", "port": 8080, "action": "allow"})
	if resp.StatusCode != 200 {
		t.Fatalf("fw add: %d %v", resp.StatusCode, out)
	}
	resp, out = g.do(t, "DELETE", "/api/nodes/"+node1+"/firewall/rules/1", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("fw delete: %d %v", resp.StatusCode, out)
	}
	waitFor(t, "ufw methods relayed", func() bool {
		return agent.got("ufw.rule.add") && agent.got("ufw.rule.delete")
	}, 5*time.Second)
	// server-side validation never reaches the agent
	resp, _ = g.do(t, "POST", "/api/nodes/"+node1+"/firewall/rules",
		map[string]any{"protocol": "tcp", "port": 0, "action": "allow"})
	if resp.StatusCode != 400 {
		t.Fatalf("bad port: expected 400, got %d", resp.StatusCode)
	}

	// firewall preview relays and returns the simulated state without applying
	resp, out = g.do(t, "POST", "/api/nodes/"+node1+"/firewall/preview",
		map[string]any{"protocol": "tcp", "port": 9090, "action": "allow"})
	if resp.StatusCode != 200 {
		t.Fatalf("fw preview: %d %v", resp.StatusCode, out)
	}
	var sim struct {
		Rules []struct {
			To string `json:"to"`
		} `json:"rules"`
	}
	raw, _ := json.Marshal(out)
	if err := json.Unmarshal(raw, &sim); err != nil || len(sim.Rules) != 1 || sim.Rules[0].To != "9090/tcp" {
		t.Fatalf("fw preview body: %v (%v)", out, err)
	}
	waitFor(t, "preview relayed", func() bool { return agent.got("ufw.rule.preview") }, 5*time.Second)

	// compose deploy + validate round-trip
	yaml := "services:\n  web:\n    image: nginx:alpine\n"
	resp, out = g.do(t, "POST", "/api/nodes/"+node1+"/compose/validate", map[string]string{"name": "web", "yaml": yaml})
	if resp.StatusCode != 200 {
		t.Fatalf("compose validate: %d %v", resp.StatusCode, out)
	}
	resp, out = g.do(t, "POST", "/api/nodes/"+node1+"/compose/deploy", map[string]string{"name": "web", "yaml": yaml})
	if resp.StatusCode != 200 {
		t.Fatalf("compose deploy: %d %v", resp.StatusCode, out)
	}
	waitFor(t, "compose deploy relayed", func() bool { return agent.got("docker.compose.deploy") }, 5*time.Second)
}

// TestStatusStreamReconnect exercises WS reconnect + cleanup: a second status
// stream must keep receiving stats after the first one closes.
func TestStatusStreamReconnect(t *testing.T) {
	agent := newFakeAgent(t, "tok", 100*time.Millisecond)
	g := newGoPit(t, nil)
	g.login(t)
	node1 := g.addNode(t, "127.0.0.1", agent.port(t), "tok")

	first := g.ws(t, "/api/nodes/"+node1+"/status")
	readStats(t, first, 5*time.Second)
	first.Close()

	second := g.ws(t, "/api/nodes/"+node1+"/status")
	readStats(t, second, 5*time.Second) // old subscription must not block the new one
	second.Close()

	// the agent conn stays healthy (one stats burst after both browsers left)
	waitFor(t, "stats flowing to a fresh subscriber", func() bool {
		third := g.ws(t, "/api/nodes/"+node1+"/status")
		defer third.Close()
		_, err := readStatsNoFail(third, 3*time.Second)
		return err == nil
	}, 10*time.Second)
}

func readStatsNoFail(ws *websocket.Conn, d time.Duration) (protocol.SystemStats, error) {
	ws.SetReadDeadline(time.Now().Add(d))
	for {
		var e protocol.Envelope
		if err := ws.ReadJSON(&e); err != nil {
			return protocol.SystemStats{}, err
		}
		if e.Method == "system.stats" {
			var st protocol.SystemStats
			if err := json.Unmarshal(e.Payload, &st); err != nil {
				return protocol.SystemStats{}, err
			}
			return st, nil
		}
	}
}
