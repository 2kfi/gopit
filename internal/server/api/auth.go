package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nbutton23/zxcvbn-go"
	"golang.org/x/crypto/bcrypt"

	"gopit/internal/server/store"
	"gopit/internal/server/webhooks"
)

// AuthAPI serves /api/login, /api/logout, /api/users, /api/password.
type AuthAPI struct {
	store    *store.Store
	auth     *Auth
	csrf     *CSRFToken
	minScore int // minimum password strength (zxcvbn 0-4); enforced on create/change
	hooks    *webhooks.Store
}

// NewAuthAPI wires the auth routes.
func NewAuthAPI(s *store.Store, a *Auth, csrf *CSRFToken, minScore int, hooks *webhooks.Store) *AuthAPI {
	return &AuthAPI{store: s, auth: a, csrf: csrf, minScore: minScore, hooks: hooks}
}

type creds struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type pwChange struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// loginLimiter throttles failed credential checks per IP, resetting the
// window lazily on access. Successful logins don't count (NAT users must not
// lock each other out). ponytail: single-process map; move to Redis/nginx
// when multi-server. Trusts X-Forwarded-For, so strip it when directly
// exposed.
type loginLimiter struct {
	mu          sync.Mutex
	limit       int
	window      time.Duration
	windowStart time.Time
	failures    map[string]int
}

func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.windowStart) >= l.window {
		l.windowStart = time.Now()
		l.failures = make(map[string]int)
	}
	return l.failures[ip] >= l.limit
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.windowStart) >= l.window {
		l.windowStart = time.Now()
		l.failures = make(map[string]int)
	}
	l.failures[ip]++
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

var loginRL = &loginLimiter{limit: 10, window: 15 * time.Minute, windowStart: time.Now(), failures: map[string]int{}}

// clientIP extracts the caller IP, honoring the proxy's X-Forwarded-For.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Login verifies credentials and sets the auth cookie.
func (a *AuthAPI) Login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if loginRL.blocked(ip) {
		writeErr(w, http.StatusTooManyRequests, "too many attempts")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var c creds
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil || c.Username == "" || c.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password required")
		return
	}
	u, err := a.store.GetUserByUsername(c.Username)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(c.Password)) != nil {
		loginRL.fail(ip)
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	loginRL.reset(ip)
	token, err := a.auth.Sign(u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "sign failed")
		return
	}
	csrfToken := a.csrf.Generate()
	cookie := &http.Cookie{
		Name: cookieName, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(a.auth.ttl.Seconds()),
	}
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		cookie.Secure = true
	}
	http.SetCookie(w, cookie)
	a.hooks.Fire(webhooks.EventUserLogin, map[string]string{"username": c.Username})
	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "username": u.Username, "csrf_token": csrfToken})
}

// Logout clears the auth cookie.
func (a *AuthAPI) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Me returns the current authenticated user's ID and username.
func (a *AuthAPI) Me(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r)
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	t := a.csrf.Current()
	if t == "" {
		t = a.csrf.Generate()
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "username": u.Username, "csrf_token": t})
}

// CreateUser adds a user (admin only).
func (a *AuthAPI) CreateUser(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var c creds
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil || c.Username == "" || utf8.RuneCountInString(c.Password) < 6 {
		writeErr(w, http.StatusBadRequest, "username required, password >= 6 chars")
		return
	}
	if score := passwordScore(c.Password); score < a.minScore {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("password too weak (score %d/%d, need %d): add length, mixed case, digits or symbols", score, 4, a.minScore))
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hash failed")
		return
	}
	id, err := a.store.CreateUser(c.Username, string(hash))
	if errors.Is(err, store.ErrExists) {
		writeErr(w, http.StatusConflict, "username exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db failed")
		return
	}
	a.hooks.Fire(webhooks.EventUserCreated, map[string]any{"id": id, "username": c.Username})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "username": c.Username})
}

// ListUsers lists all users (admin only).
func (a *AuthAPI) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.store.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db failed")
		return
	}
	writeJSON(w, http.StatusOK, users)
}

// DeleteUser removes a user (admin only).
func (a *AuthAPI) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil || id == 1 {
		writeErr(w, http.StatusBadRequest, "cannot delete bootstrap admin")
		return
	}
	if err := a.store.DeleteUser(id); err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	a.hooks.Fire(webhooks.EventUserDeleted, map[string]int64{"id": id})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ChangePassword updates the caller's own password.
func (a *AuthAPI) ChangePassword(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	u := UserFrom(r)
	var p pwChange
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || utf8.RuneCountInString(p.NewPassword) < 6 {
		writeErr(w, http.StatusBadRequest, "new password >= 6 chars")
		return
	}
	if score := passwordScore(p.NewPassword); score < a.minScore {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("new password too weak (score %d/%d, need %d): add length, mixed case, digits or symbols", score, 4, a.minScore))
		return
	}
	dbUser, err := a.store.GetUserByUsername(u.Username)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(dbUser.PasswordHash), []byte(p.OldPassword)) != nil {
		writeErr(w, http.StatusUnauthorized, "old password incorrect")
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(p.NewPassword), bcrypt.DefaultCost)
	if err := a.store.UpdateUserPassword(u.ID, string(hash)); err != nil {
		writeErr(w, http.StatusInternalServerError, "db failed")
		return
	}
	a.hooks.Fire(webhooks.EventPasswordChange, map[string]string{"username": u.Username})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// passwordScore rates a password with zxcvbn (0-4). A caller-declared
// "no secret, junk" password (e.g. "aaaaaaaa") scores 0; a long mixed one
// scores 3-4. min_score default 2 keeps weak but long passwords out without
// demanding symbols.
func passwordScore(pw string) int {
	return zxcvbn.PasswordStrength(pw, nil).Score
}

// RotateJWT generates a new JWT secret, invalidates all sessions by persisting
// the new secret, and returns it so the operator can distribute it (e.g. via
// config file or secret manager). Admin only.
func (a *AuthAPI) RotateJWT(w http.ResponseWriter, r *http.Request) {
	newSecret := make([]byte, 32)
	if _, err := rand.Read(newSecret); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to generate secret")
		return
	}
	secretStr := hex.EncodeToString(newSecret)
	if err := a.store.SetSetting("jwt_secret", secretStr); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to persist secret")
		return
	}
	a.auth.secret = []byte(secretStr)
	writeJSON(w, http.StatusOK, map[string]string{"jwt_secret": secretStr})
}
