package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"gopit/internal/server/store"
)

// AuthAPI serves /api/login, /api/logout, /api/users, /api/password.
type AuthAPI struct {
	store *store.Store
	auth  *Auth
}

// NewAuthAPI wires the auth routes.
func NewAuthAPI(s *store.Store, a *Auth) *AuthAPI {
	return &AuthAPI{store: s, auth: a}
}

type creds struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type pwChange struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// Login verifies credentials and sets the auth cookie.
func (a *AuthAPI) Login(w http.ResponseWriter, r *http.Request) {
	var c creds
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	u, err := a.store.GetUserByUsername(c.Username)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(c.Password)) != nil {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, err := a.auth.Sign(u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "sign failed")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(a.auth.ttl.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "username": u.Username})
}

// Logout clears the auth cookie.
func (a *AuthAPI) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// CreateUser adds a user (admin only).
func (a *AuthAPI) CreateUser(w http.ResponseWriter, r *http.Request) {
	var c creds
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil || c.Username == "" || len(c.Password) < 6 {
		writeErr(w, http.StatusBadRequest, "username required, password >= 6 chars")
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ChangePassword updates the caller's own password.
func (a *AuthAPI) ChangePassword(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r)
	var p pwChange
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || len(p.NewPassword) < 6 {
		writeErr(w, http.StatusBadRequest, "new password >= 6 chars")
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
