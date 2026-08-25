package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"gopit/internal/protocol"
	"gopit/internal/server/discovery"
	"gopit/internal/server/nodemanager"
	"gopit/internal/server/store"
	"gopit/internal/server/webhooks"
)

var testIPSeq atomic.Int64

func nextTestIP() string {
	n := testIPSeq.Add(1)
	return fmt.Sprintf("10.99.%d.%d", (n>>8)&0xff, n&0xff)
}

const testSecret = "test-secret"

// env is a full HTTP server on the real router (auth, CSRF, rate limiting,
// health, metrics, admin routes), backed by a fresh store.
type env struct {
	s      *store.Store
	m      *nodemanager.Manager
	ts     *httptest.Server
	origin string
}

func newEnv(t *testing.T, rateLimitPerMin int) *env {
	return newEnvTrust(t, rateLimitPerMin, false)
}

// newEnvTrust builds an env with an explicit trust_proxy setting; the
// limiter test needs true so its XFF-spoofed failures stay isolated from
// other tests sharing the package-global loginLimiter.
func newEnvTrust(t *testing.T, rateLimitPerMin int, trustProxy bool) *env {
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
	h := Router(s, m, disc, testSecret, "pair-tok", false, trustProxy, rateLimitPerMin, 0, webhooks.New(nil))
	ts := httptest.NewServer(h)
	hash, _ := bcrypt.GenerateFromPassword([]byte("adminpass"), bcrypt.MinCost)
	if _, err := s.CreateUser("admin", string(hash)); err != nil {
		t.Fatal(err)
	}
	e := &env{s: s, m: m, ts: ts, origin: ts.URL}
	t.Cleanup(e.Close)
	return e
}

func (e *env) Close() {
	e.m.Close()
	e.s.Close()
	e.ts.Close()
}

type authCtx struct {
	xff    string
	cookie string
	csrf   string
	origin bool // attach Origin even without a CSRF token (CSRF enforcement tests)
}

// do runs one request. When a is non-nil, the auth cookie and IP are
// attached; a.origin + a.csrf attach Origin and (when set) X-CSRF-Token so
// mutating requests exercise the CSRF middleware like a browser would.
func (e *env) do(t *testing.T, method, path string, body any, a *authCtx) (*http.Response, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, e.ts.URL+path, rdr)
	if a != nil {
		req.Header.Set("X-Forwarded-For", a.xff)
		if a.cookie != "" {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: a.cookie})
		}
		if a.origin || a.csrf != "" {
			req.Header.Set("Origin", e.origin)
		}
		if a.csrf != "" {
			req.Header.Set("X-CSRF-Token", a.csrf)
		}
	}
	rr := httptest.NewRecorder()
	e.ts.Config.Handler.ServeHTTP(rr, req)
	out := map[string]any{}
	json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Result(), out
}

// login authenticates as admin and returns a context carrying the JWT cookie
// and CSRF token, as the browser would hold them.
func (e *env) login(t *testing.T) *authCtx { return e.loginAs(t, "admin", "adminpass") }

func (e *env) loginAs(t *testing.T, username, password string) *authCtx {
	t.Helper()
	a := &authCtx{xff: nextTestIP()}
	resp, out := e.do(t, "POST", "/api/login", map[string]string{"username": username, "password": password}, a)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s failed: %d %v", username, resp.StatusCode, out)
	}
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			a.cookie = c.Value
		}
	}
	a.csrf, _ = out["csrf_token"].(string)
	if a.cookie == "" || a.csrf == "" {
		t.Fatal("login response missing cookie or csrf_token")
	}
	return a
}

func TestLoginLogoutFlow(t *testing.T) {
	e := newEnv(t, 100)
	a := e.login(t)

	resp, _ := e.do(t, "GET", "/api/me", nil, a)
	if resp.StatusCode != 200 {
		t.Fatalf("me: %d", resp.StatusCode)
	}

	resp, _ = e.do(t, "POST", "/api/logout", nil, a) // no Origin: CSRF not enforceable
	if resp.StatusCode != 200 {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	sc := resp.Header.Get("Set-Cookie")
	if !strings.Contains(sc, "gopit_token=;") || !strings.Contains(sc, "Max-Age=0") {
		t.Fatalf("logout must clear the cookie, got %q", sc)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	e := newEnv(t, 100)
	for _, c := range []map[string]string{
		{"username": "admin", "password": "wrong"},
		{"username": "nobody", "password": "wrong"},
	} {
		resp, out := e.do(t, "POST", "/api/login", c, &authCtx{xff: nextTestIP()})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("login %v: expected 401, got %d (%v)", c, resp.StatusCode, out)
		}
	}
}

func TestLoginRateLimitEnforced(t *testing.T) {
	e := newEnvTrust(t, 100, true)
	ip := nextTestIP()
	a := &authCtx{xff: ip}
	for i := 0; i < loginRL.limit; i++ {
		resp, _ := e.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": "wrong"}, a)
		if resp.StatusCode != 401 {
			t.Fatalf("attempt %d: expected 401, got %d", i, resp.StatusCode)
		}
	}
	resp, out := e.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": "adminpass"}, a)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("blocked IP with valid creds: expected 429, got %d (%v)", resp.StatusCode, out)
	}
	// an unrelated IP must not be affected
	resp, _ = e.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": "wrong"}, &authCtx{xff: nextTestIP()})
	if resp.StatusCode != 401 {
		t.Fatalf("unrelated IP must not be blocked, got %d", resp.StatusCode)
	}
	loginRL.mu.Lock()
	loginRL.failures = map[string]int{}
	loginRL.windowStart = time.Now()
	loginRL.mu.Unlock()
}

func TestAPIRateLimitMiddleware(t *testing.T) {
	e := newEnv(t, 5) // burst = 5/5+1 = 2
	a := &authCtx{xff: nextTestIP()}
	codes := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		resp, _ := e.do(t, "GET", "/api/nodes", nil, a)
		codes = append(codes, resp.StatusCode)
	}
	if codes[0] != 401 || codes[1] != 401 {
		t.Fatalf("burst requests must pass auth, got %v", codes)
	}
	if codes[2] != 429 || codes[3] != 429 {
		t.Fatalf("expect 429 past the burst, got %v", codes)
	}
}

func TestCSRFEnforcedOnStateChangingRequests(t *testing.T) {
	e := newEnv(t, 100)
	a := e.login(t)

	// browser Origin present, no token -> 403
	b := *a
	b.csrf = ""
	b.origin = true
	resp, _ := e.do(t, "POST", "/api/nodes", map[string]any{"ip": "10.0.0.5", "port": 1221, "token": "x"}, &b)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF token with Origin: expected 403, got %d", resp.StatusCode)
	}
	// with Origin and the token, the request reaches the handler (body fails its own validation)
	resp, _ = e.do(t, "POST", "/api/nodes", nil, a)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("valid CSRF must pass middleware (400 from handler expected), got %d", resp.StatusCode)
	}
	// non-browser requests (no Origin) skip CSRF but still authenticate
	resp, _ = e.do(t, "POST", "/api/nodes", map[string]any{"ip": "10.0.0.5", "port": 1221, "token": "x"}, &authCtx{xff: a.xff, cookie: a.cookie})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("no-Origin POST must not need CSRF, got %d", resp.StatusCode)
	}
}

func TestOriginMismatchRejected(t *testing.T) {
	e := newEnv(t, 100)
	a := e.login(t)
	req := httptest.NewRequest("POST", e.ts.URL+"/api/logout", nil)
	req.Header.Set("X-Forwarded-For", a.xff)
	req.Header.Set("Origin", "http://evil.example")
	rr := httptest.NewRecorder()
	e.ts.Config.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST: expected 403, got %d", rr.Code)
	}
}

func TestAdminOnlyEndpoints(t *testing.T) {
	e := newEnv(t, 100)
	admin := e.login(t)
	resp, out := e.do(t, "POST", "/api/users", map[string]string{"username": "bob", "password": "bobpass1"}, admin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create user: %d %v", resp.StatusCode, out)
	}
	bob := &authCtx{xff: nextTestIP()}
	resp, _ = e.do(t, "POST", "/api/login", map[string]string{"username": "bob", "password": "bobpass1"}, bob)
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			bob.cookie = c.Value
		}
	}

	for _, path := range []string{"/api/users", "/api/admin/jwt/rotate"} {
		method := "GET"
		if path == "/api/admin/jwt/rotate" {
			method = "POST"
		}
		resp, _ := e.do(t, method, path, nil, bob)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("non-admin %s %s: expected 403, got %d", method, path, resp.StatusCode)
		}
	}
	resp, _ = e.do(t, "GET", "/api/users", nil, admin)
	if resp.StatusCode != 200 {
		t.Fatalf("admin list users: %d", resp.StatusCode)
	}
}

func TestExpiredJWTRejected(t *testing.T) {
	e := newEnv(t, 100)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserID: 1, Username: "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "gopit", ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		},
	})
	s, err := tok.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := e.do(t, "GET", "/api/me", nil, &authCtx{xff: nextTestIP(), cookie: s})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired JWT: expected 401, got %d", resp.StatusCode)
	}
}

func TestJWTRotateInvalidatesSessions(t *testing.T) {
	e := newEnv(t, 100)
	admin := e.login(t)

	resp, out := e.do(t, "POST", "/api/admin/jwt/rotate", nil, admin)
	if resp.StatusCode != 200 {
		t.Fatalf("rotate: %d %v", resp.StatusCode, out)
	}
	secret, _ := out["jwt_secret"].(string)
	if len(secret) != 64 {
		t.Fatalf("expected a 64-hex-char secret, got %q", secret)
	}
	resp, _ = e.do(t, "GET", "/api/me", nil, admin)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old session after rotation: expected 401, got %d", resp.StatusCode)
	}
	// a fresh login works with the rotated secret (persisted in the store)
	re := e.login(t)
	if re.cookie == admin.cookie {
		t.Fatal("new login must mint a token under the rotated secret")
	}
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t, 100)
	admin := e.login(t)

	resp, _ := e.do(t, "PUT", "/api/password", map[string]string{"old_password": "adminpass", "new_password": "newpass6"}, admin)
	if resp.StatusCode != 200 {
		t.Fatalf("change password: %d", resp.StatusCode)
	}
	resp, _ = e.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": "adminpass"}, &authCtx{xff: nextTestIP()})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password must fail, got %d", resp.StatusCode)
	}
	resp, _ = e.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": "newpass6"}, &authCtx{xff: nextTestIP()})
	if resp.StatusCode != 200 {
		t.Fatalf("new password must work, got %d", resp.StatusCode)
	}
	admin = e.loginAs(t, "admin", "newpass6") // login above rotated the shared CSRF token; re-fetch it
	// wrong old password -> 401
	resp, _ = e.do(t, "PUT", "/api/password", map[string]string{"old_password": "wrong", "new_password": "xyz123"}, admin)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong old password: expected 401, got %d", resp.StatusCode)
	}
	// new password over bcrypt's 72-byte limit -> 400, account still usable
	long := strings.Repeat("a", 73)
	admin = e.loginAs(t, "admin", "newpass6")
	resp, _ = e.do(t, "PUT", "/api/password", map[string]string{"old_password": "newpass6", "new_password": long}, admin)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("73-byte password: expected 400, got %d", resp.StatusCode)
	}
	resp, _ = e.do(t, "POST", "/api/login", map[string]string{"username": "admin", "password": "newpass6"}, &authCtx{xff: nextTestIP()})
	if resp.StatusCode != 200 {
		t.Fatalf("account must survive rejected change, got %d", resp.StatusCode)
	}
}

func TestDeleteUserInvalidatesSessions(t *testing.T) {
	e := newEnv(t, 100)
	admin := e.login(t)
	resp, out := e.do(t, "POST", "/api/users", map[string]string{"username": "carol", "password": "carolpass"}, admin)
	id := int64(out["id"].(float64))
	carol := &authCtx{xff: nextTestIP()}
	loginResp, _ := e.do(t, "POST", "/api/login", map[string]string{"username": "carol", "password": "carolpass"}, carol)
	for _, c := range loginResp.Cookies() {
		if c.Name == cookieName {
			carol.cookie = c.Value
		}
	}
	resp, _ = e.do(t, "GET", "/api/me", nil, carol)
	if resp.StatusCode != 200 {
		t.Fatalf("carol session must work before deletion, got %d", resp.StatusCode)
	}
	admin = e.login(t) // carol's login rotated the shared CSRF token; re-fetch it
	resp, out = e.do(t, "DELETE", fmt.Sprintf("/api/users/%d", id), nil, admin)
	if resp.StatusCode != 200 {
		t.Fatalf("delete carol: %d %v", resp.StatusCode, out)
	}
	resp, _ = e.do(t, "GET", "/api/me", nil, carol)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("deleted user's session must die, got %d", resp.StatusCode)
	}
	// the bootstrap admin is undeletable
	resp, _ = e.do(t, "DELETE", "/api/users/1", nil, admin)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("deleting the bootstrap admin: expected 400, got %d", resp.StatusCode)
	}
}

func TestCreateUserValidation(t *testing.T) {
	e := newEnv(t, 100)
	admin := e.login(t)
	for _, c := range []map[string]string{
		{"username": "x", "password": "short"},
		{"username": ""},
	} {
		resp, _ := e.do(t, "POST", "/api/users", c, admin)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("create %v: expected 400, got %d", c, resp.StatusCode)
		}
	}
	resp, _ := e.do(t, "POST", "/api/users", map[string]string{"username": "dave", "password": "davepass"}, admin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("valid create: %d", resp.StatusCode)
	}
	resp, _ = e.do(t, "POST", "/api/users", map[string]string{"username": "dave", "password": "davepass"}, admin)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate username: expected 409, got %d", resp.StatusCode)
	}
}

func TestHealthAndMetrics(t *testing.T) {
	e := newEnv(t, 100)
	resp, out := e.do(t, "GET", "/api/health", nil, nil)
	if resp.StatusCode != 200 || out["db"] != "ok" || out["nodes_total"].(float64) != 0 {
		t.Fatalf("health: %d %v", resp.StatusCode, out)
	}
	if err := e.s.UpsertNode(&store.Node{ID: "n1", Hostname: "h", IP: "1.2.3.4", Port: 1221, FirstSeen: "t", LastSeen: "t"}); err != nil {
		t.Fatal(err)
	}
	resp, out = e.do(t, "GET", "/api/health", nil, nil)
	if out["nodes_total"].(float64) != 1 {
		t.Fatalf("health after node: %v", out)
	}

	req := httptest.NewRequest("GET", e.ts.URL+"/metrics", nil)
	rr := httptest.NewRecorder()
	e.ts.Config.Handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("metrics: %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"gopit_http_requests_total", "gopit_nodes_total", "go_goroutines"} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q", want)
		}
	}
}
