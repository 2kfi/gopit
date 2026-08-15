package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"gopit/internal/server/store"
	"gopit/internal/server/webhooks"
)

// SetupStatus reports whether the server has been configured (a user exists).
// Unauthenticated so the first-run UI can decide between setup and login.
func (a *AuthAPI) SetupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := a.store.CountUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": n > 0})
}

// Setup creates the first admin account on an unconfigured server. It only
// succeeds while the user table is empty; afterwards every attempt is a 409.
func (a *AuthAPI) Setup(w http.ResponseWriter, r *http.Request) {
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
	n, err := a.store.CountUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db failed")
		return
	}
	if n > 0 {
		writeErr(w, http.StatusConflict, "server already configured")
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
