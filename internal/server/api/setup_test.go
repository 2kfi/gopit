package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gopit/internal/protocol"
	"gopit/internal/server/discovery"
	"gopit/internal/server/nodemanager"
	"gopit/internal/server/store"
	"gopit/internal/server/webhooks"
)

// setupEnv is newEnv but with an empty user table (unconfigured server).
func setupEnv(t *testing.T, passwordMinScore int) *env {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	m := nodemanager.New(s, nodemanager.Callbacks{
		OnStatus: func(id, status string) { s.SetNodeStatus(id, status) },
		OnStats:  func(string, protocol.SystemStats) {},
	}, true)
	disc := &discovery.Client{BroadcastAddr: "127.0.0.1", Port: 1221, Timeout: 50 * time.Millisecond}
	h := Router(s, m, disc, testSecret, "pair-tok", false, false, 100, passwordMinScore, webhooks.New(nil))
	ts := httptest.NewServer(h)
	e := &env{s: s, m: m, ts: ts, origin: ts.URL}
	t.Cleanup(e.Close)
	return e
}

func TestSetupStatusReflectsUserCount(t *testing.T) {
	e := setupEnv(t, 0)
	a := &authCtx{xff: nextTestIP()}

	resp, out := e.do(t, "GET", "/api/setup/status", nil, a)
	if resp.StatusCode != http.StatusOK || out["configured"] != false {
		t.Fatalf("unconfigured: expected configured=false, got %d %v", resp.StatusCode, out)
	}

	resp, _ = e.do(t, "POST", "/api/setup", map[string]string{"username": "admin", "password": "adminpass"}, a)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: expected 201, got %d", resp.StatusCode)
	}

	resp, out = e.do(t, "GET", "/api/setup/status", nil, a)
	if resp.StatusCode != http.StatusOK || out["configured"] != true {
		t.Fatalf("configured: expected configured=true, got %d %v", resp.StatusCode, out)
	}
}

func TestSetupCreatesFirstAdmin(t *testing.T) {
	e := setupEnv(t, 0)
	a := &authCtx{xff: nextTestIP()}

	resp, out := e.do(t, "POST", "/api/setup", map[string]string{"username": "boss", "password": "hunter2x"}, a)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: expected 201, got %d %v", resp.StatusCode, out)
	}
	if out["id"] != float64(1) {
		t.Fatalf("first user must be id 1 (admin), got %v", out["id"])
	}

	// the created credentials must be able to log in
	resp, out = e.do(t, "POST", "/api/login", map[string]string{"username": "boss", "password": "hunter2x"}, &authCtx{xff: nextTestIP()})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login with created admin: expected 200, got %d %v", resp.StatusCode, out)
	}
}

func TestSetupRejectedWhenConfigured(t *testing.T) {
	e := setupEnv(t, 0)
	if resp, _ := e.do(t, "POST", "/api/setup", map[string]string{"username": "first", "password": "hunter2x"}, &authCtx{xff: nextTestIP()}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first setup should succeed, got %d", resp.StatusCode)
	}
	resp, out := e.do(t, "POST", "/api/setup", map[string]string{"username": "second", "password": "hunter2x"}, &authCtx{xff: nextTestIP()})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second setup must be rejected with 409, got %d %v", resp.StatusCode, out)
	}
}

func TestSetupValidatesInput(t *testing.T) {
	e := setupEnv(t, 2)
	for _, c := range []map[string]string{
		{"username": "admin", "password": "short"},
		{"username": "", "password": "hunter2x"},
	} {
		resp, _ := e.do(t, "POST", "/api/setup", c, &authCtx{xff: nextTestIP()})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("setup %v: expected 400, got %d", c, resp.StatusCode)
		}
	}
}
