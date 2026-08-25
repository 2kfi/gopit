package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
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

	"crypto/subtle"

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

	mu sync.RWMutex // guards secret, which RotateJWT swaps at runtime
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
	a.mu.RLock()
	secret := a.secret
	a.mu.RUnlock()
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
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
	tok, err := jwt.ParseWithClaims(c.Value, &claims, a.keyFunc, jwt.WithIssuer("gopit"))
	if err != nil || !tok.Valid {
		return nil, errors.New("invalid token")
	}
	u, err := a.store.GetUserByUsername(claims.Username)
	if err != nil {
		return nil, errors.New("user not found")
	}
	return u, nil
}

// keyFunc resolves the HMAC verification key, honoring runtime rotation.
func (a *Auth) keyFunc(t *jwt.Token) (any, error) {
	if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
		return nil, errors.New("bad signing method")
	}
	a.mu.RLock()
	secret := a.secret
	a.mu.RUnlock()
	return secret, nil
}

// csrfFor derives the CSRF token bound to one session JWT.
func (a *Auth) csrfFor(sessionJWT string) string {
	a.mu.RLock()
	secret := a.secret
	a.mu.RUnlock()
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(sessionJWT))
	return hex.EncodeToString(mac.Sum(nil))
}

// CSRFToken returns this request's session-bound CSRF token ("" when there is
// no session cookie). Stateless: derived from the JWT, never stored.
func (a *Auth) CSRFToken(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	return a.csrfFor(c.Value)
}

// identify returns the username from the session cookie without hitting the
// store; for audit logging only.
func (a *Auth) identify(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	var claims Claims
	tok, err := jwt.ParseWithClaims(c.Value, &claims, a.keyFunc, jwt.WithIssuer("gopit"))
	if err != nil || !tok.Valid {
		return ""
	}
	return claims.Username
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
	mu        sync.Mutex
	visitors  map[string]*visitor
	rate      rate.Limit
	burst     int
	lastSweep time.Time
}

type visitor struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// visitorTTL bounds how long an idle IP stays in the map; without it the map
// grows without bound on internet-exposed servers.
const visitorTTL = 10 * time.Minute

// NewRateLimiter creates a limiter with requests per minute and burst.
func NewRateLimiter(reqPerMin, burst int) *RateLimiter {
	return &RateLimiter{
		visitors: make(map[string]*visitor),
		rate:     rate.Limit(float64(reqPerMin) / 60.0),
		burst:    burst,
	}
}

func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.sweepLocked(now)
	v, ok := rl.visitors[ip]
	if !ok {
		v = &visitor{lim: rate.NewLimiter(rl.rate, rl.burst)}
		rl.visitors[ip] = v
	}
	v.lastSeen = now
	return v.lim
}

// sweepLocked drops idle visitors; runs at most once per TTL/2 so the cost
// stays amortized O(1) per request.
func (rl *RateLimiter) sweepLocked(now time.Time) {
	if now.Sub(rl.lastSweep) < visitorTTL/2 {
		return
	}
	rl.lastSweep = now
	for ip, v := range rl.visitors {
		if now.Sub(v.lastSeen) > visitorTTL {
			delete(rl.visitors, ip)
		}
	}
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

// CSRFMiddleware rejects state-changing browser requests whose X-CSRF-Token
// (or _csrf form field) does not match this session's token — an HMAC of the
// session JWT under the JWT secret, so it is stateless and per-session:
// concurrent logins never invalidate each other's token. Only enforced when
// an Origin header is present (browser requests).
func CSRFMiddleware(a *Auth) func(http.Handler) http.Handler {
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
			expected := a.CSRFToken(r)
			got := r.Header.Get("X-CSRF-Token")
			if got == "" {
				got = r.FormValue("_csrf")
			}
			// Constant-time compare: the token gates every mutating request.
			if expected == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(got)) != 1 {
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
		uid := any("-")
		if username := a.identify(r); username != "" {
			uid = username
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
