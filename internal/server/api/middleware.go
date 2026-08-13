package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

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

// verify parses the JWT cookie.
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
	})
	if err != nil || !tok.Valid {
		return nil, errors.New("invalid token")
	}
	return &store.User{ID: claims.UserID, Username: claims.Username}, nil
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
