package server_test

// Load/soak: 100 simulated nodes against one fake agent, live 1s stats
// streams, 10 concurrent terminal sessions and 50 docker log streams. The
// test asserts no goroutines leak after full teardown.

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"gopit/internal/server/store"
)

func TestLoad100NodesSoak(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	const nodes = 100
	agent := newFakeAgent(t, "load-tok", time.Second)
	g := newGoPit(t, nil)
	g.login(t)
	for i := 0; i < nodes; i++ {
		id := fmt.Sprintf("node-%03d", i)
		if err := g.s.UpsertNode(&store.Node{
			ID: id, Hostname: id, IP: "127.0.0.1", Port: agent.port(t),
			Status: store.StatusApproved, FirstSeen: "now", LastSeen: "now", Token: "load-tok",
		}); err != nil {
			t.Fatal(err)
		}
	}
	g.m.Reconcile()
	waitFor(t, "100 nodes connected", func() bool { return g.m.Count() == nodes }, 15*time.Second)

	// 10 concurrent status streams (1s stats each)
	stats := make([]*websocket.Conn, 0, 10)
	for i := 0; i < 10; i++ {
		c := g.ws(t, "/api/nodes/"+fmt.Sprintf("node-%03d", i)+"/status")
		stats = append(stats, c)
	}
	// 10 concurrent terminals (echo sessions)
	terms := make([]*websocket.Conn, 0, 10)
	for i := 0; i < 10; i++ {
		c := g.ws(t, "/api/nodes/"+fmt.Sprintf("node-%03d", i+10)+"/terminal")
		c.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err := c.WriteJSON(map[string]string{"user": "alice", "password": "pw"}); err != nil {
			t.Fatal(err)
		}
		var ack map[string]string
		if err := c.ReadJSON(&ack); err != nil || ack["type"] != "open" {
			t.Fatalf("terminal %d ack: %v %v", i, ack, err)
		}
		terms = append(terms, c)
	}
	// 50 docker log streams
	logs := make([]*websocket.Conn, 0, 50)
	for i := 0; i < 50; i++ {
		c := g.ws(t, "/api/nodes/"+fmt.Sprintf("node-%03d", i+20)+"/containers/c1/logs")
		logs = append(logs, c)
	}
	// the relay is asynchronous: the last of the 50 handshakes can land a few
	// ms after the final dial, so poll instead of asserting instantly
	waitFor(t, "50 agent log streams", func() bool { return agent.streamCount() == 50 }, 5*time.Second)

	// warm up the stats stream, then soak for a few seconds
	for _, c := range stats {
		if _, err := readStatsNoFail(c, 5*time.Second); err != nil {
			t.Fatalf("stats stream: %v", err)
		}
	}
	baseline := runtime.NumGoroutine()
	time.Sleep(3 * time.Second)

	// everything still alive and flowing after the soak
	for _, c := range stats {
		if _, err := readStatsNoFail(c, 5*time.Second); err != nil {
			t.Fatalf("stats stream after soak: %v", err)
		}
	}
	for _, c := range logs {
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, _, err := c.NextReader(); err != nil {
			t.Fatalf("log stream after soak: %v", err)
		}
	}
	for i, c := range terms {
		if err := c.WriteMessage(websocket.BinaryMessage, []byte("ping")); err != nil {
			t.Fatalf("terminal %d write: %v", i, err)
		}
		mt, b, err := c.ReadMessage()
		if err != nil || mt != websocket.BinaryMessage || string(b) != "ping" {
			t.Fatalf("terminal %d echo: %v %q", i, err, b)
		}
	}

	// teardown everything and verify no goroutine leaks
	for _, c := range stats {
		c.Close()
	}
	for _, c := range logs {
		c.Close()
	}
	for _, c := range terms {
		c.Close()
	}
	waitFor(t, "log streams cancelled", func() bool { return agent.streamCount() == 0 }, 10*time.Second)
	g.m.Close()
	agent.Close()

	waitFor(t, "goroutines back to baseline", func() bool {
		return runtime.NumGoroutine() <= baseline+5
	}, 15*time.Second)
}
