package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
	_, a := newAuthPair(t)
	u := &store.User{ID: 1, Username: "admin"}
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
