package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"golang.org/x/time/rate"
	"gopit/internal/server/store"
)

type ctxKey int

const (
	ctxUserID ctxKey = iota
	ctxUsername
	cookieName = "gopit_token"
)

// Claims is the JWT payload.
type Claims struct {
	UserID   int64  `json:"uid"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

// Auth issues signed tokens and protects routes via cookie.
type Auth struct {
	store  *store.Store
	secret []byte
	ttl    time.Duration
}

// NewAuth creates the authenticator.
func NewAuth(s *store.Store, secret string) *Auth {
	return &Auth{store: s, secret: []byte(secret), ttl: 12 * time.Hour}
}

// Sign issues a token for the user.
func (a *Auth) Sign(user *store.User) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: user.ID, Username: user.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "gopit",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(a.ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.secret)
}

// Middleware rejects unauthenticated requests with 401.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := a.verify(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUserID, user.ID)
		ctx = context.WithValue(ctx, ctxUsername, user.Username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// verify parses the JWT cookie and confirms the user still exists.
func (a *Auth) verify(r *http.Request) (*store.User, error) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil, errors.New("no cookie")
	}
	var claims Claims
	tok, err := jwt.ParseWithClaims(c.Value, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("bad signing method")
		}
		return a.secret, nil
	}, jwt.WithIssuer("gopit"))
	if err != nil || !tok.Valid {
		return nil, errors.New("invalid token")
	}
	u, err := a.store.GetUserByUsername(claims.Username)
	if err != nil {
		return nil, errors.New("user not found")
	}
	return u, nil
}

// UserFrom returns the authenticated user, or nil if not authenticated.
func UserFrom(r *http.Request) *store.User {
	id, ok := r.Context().Value(ctxUserID).(int64)
	if !ok {
		return nil
	}
	username, _ := r.Context().Value(ctxUsername).(string)
	return &store.User{ID: id, Username: username}
}

// AdminOnly rejects non-admin calls; phase 1 treats the first user as admin.
func (a *Auth) AdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := UserFrom(r)
		if u == nil || u.ID != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimiter implements a token bucket per IP for global API rate limiting.
type RateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*rate.Limiter
	rate     rate.Limit
	burst    int
}

// NewRateLimiter creates a limiter with requests per minute and burst.
func NewRateLimiter(reqPerMin, burst int) *RateLimiter {
	return &RateLimiter{
		visitors: make(map[string]*rate.Limiter),
		rate:     rate.Limit(float64(reqPerMin) / 60.0),
		burst:    burst,
	}
}

func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	lim, ok := rl.visitors[ip]
	if !ok {
		lim = rate.NewLimiter(rl.rate, rl.burst)
		rl.visitors[ip] = lim
	}
	return lim
}

// Middleware applies per-IP rate limiting to /api routes.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		ip := clientIP(r)
		if !rl.getLimiter(ip).Allow() {
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CSRFToken is the double-submit cookie + header token.
type CSRFToken struct {
	mu    sync.Mutex
	token string
}

// NewCSRFToken creates a new token generator.
func NewCSRFToken() *CSRFToken {
	return &CSRFToken{}
}

// Generate creates a new CSRF token (32 bytes hex).
func (c *CSRFToken) Generate() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := make([]byte, 32)
	rand.Read(b)
	c.token = hex.EncodeToString(b)
	return c.token
}

// Current returns the active token.
func (c *CSRFToken) Current() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// Validate checks if the provided token matches the current one.
func (c *CSRFToken) Validate(token string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token != "" && c.token == token
}

// CSRFMiddleware validates double-submit CSRF token for state-changing browser requests.
// Only enforced when Origin header is present (browser requests).
func CSRFMiddleware(csrf *CSRFToken) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			// Check double-submit: header X-CSRF-Token or form field _csrf
			token := r.Header.Get("X-CSRF-Token")
			if token == "" {
				token = r.FormValue("_csrf")
			}
			if !csrf.Validate(token) {
				writeErr(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func parseIDParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/users/"), 10, 64)
}

// AuditLog logs mutating API requests: method, path, user, status, duration.
func (a *Auth) AuditLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		user, _ := a.verify(r) // identity for logging only; nil when anonymous
		uid := any("-")
		if user != nil {
			uid = user.ID
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("api request",
			"method", r.Method, "path", r.URL.Path, "user", uid,
			"status", rec.status, "duration", time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// originCheck rejects cross-site state-changing requests (CSRF), enforced
// only when a browser-sent Origin header is present.
// Defense-in-depth alongside double-submit CSRF token.
func originCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			if origin, err := url.Parse(r.Header.Get("Origin")); err == nil && origin.Host != "" && !strings.EqualFold(origin.Host, r.Host) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
