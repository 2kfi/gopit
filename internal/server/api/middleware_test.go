package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gopit/internal/server/store"
)

func newAuthPair(t *testing.T) (*store.Store, *Auth) {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a := NewAuth(s, "test-secret")
	return s, a
}

func TestAuthMiddlewareValidToken(t *testing.T) {
	s, a := newAuthPair(t)
	u := &store.User{ID: 1, Username: "admin"}
	if _, err := s.CreateUser(u.Username, "hash"); err != nil {
		t.Fatal(err)
	}
	token, err := a.Sign(u)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/nodes", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	rr := httptest.NewRecorder()
	got := ""
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = UserFrom(r).Username
		w.WriteHeader(200)
	})).ServeHTTP(rr, req)
	if rr.Code != 200 || got != "admin" {
		t.Fatalf("expected 200 + admin, got %d %q", rr.Code, got)
	}
}

func TestAuthMiddlewareInvalidToken(t *testing.T) {
	_, a := newAuthPair(t)
	req := httptest.NewRequest("GET", "/api/nodes", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "garbage"})
	rr := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})).ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestAuthMiddlewareNoCookie(t *testing.T) {
	_, a := newAuthPair(t)
	req := httptest.NewRequest("GET", "/api/nodes", nil)
	rr := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})).ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestAdminOnly(t *testing.T) {
	_, a := newAuthPair(t)
	// non-admin id 2 must be rejected
	u2 := &store.User{ID: 2, Username: "bob"}
	tok2, _ := a.Sign(u2)
	req := httptest.NewRequest("GET", "/api/users", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: tok2})
	rr := httptest.NewRecorder()
	a.AdminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})).ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

func TestLoginLimiter(t *testing.T) {
	l := &loginLimiter{limit: 3, window: time.Minute, windowStart: time.Now(), failures: map[string]int{}}
	ip := "1.2.3.4"
	for i := 0; i < 3; i++ {
		l.fail(ip)
	}
	if !l.blocked(ip) {
		t.Fatal("expected IP to be blocked after 3 failures")
	}
	if l.blocked("5.6.7.8") {
		t.Fatal("unrelated IP must not be blocked")
	}
	l.reset(ip)
	if l.blocked(ip) {
		t.Fatal("reset must clear the failure count")
	}

	// window expiry unblocks without any explicit reset
	old := &loginLimiter{limit: 3, window: time.Minute, windowStart: time.Now().Add(-2 * time.Minute), failures: map[string]int{"9.9.9.9": 3}}
	if old.blocked("9.9.9.9") {
		t.Fatal("expired window must unblock")
	}
}
