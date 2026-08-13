// Package store is the SQLite persistence layer (modernc.org/sqlite, pure Go).
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Node statuses.
const (
	StatusDiscovered = "discovered"
	StatusPending    = "pending"
	StatusApproved   = "approved"
	StatusOffline    = "offline"
	StatusOnline     = "online"
)

// User is a dashboard account.
type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	CreatedAt    string `json:"created_at"`
}

// Node is a managed machine.
type Node struct {
	ID           string `json:"id"` // UUID
	Hostname     string `json:"hostname"`
	IP           string `json:"ip"`
	Port         int    `json:"port"`
	Status       string `json:"status"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`
	FirstSeen    string `json:"first_seen"`
	LastSeen     string `json:"last_seen"`
	TLS          bool   `json:"tls"` // agent serves wss
	Token        string `json:"-"`
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating) the database at path and runs migrations.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS nodes (
			id TEXT PRIMARY KEY,
			hostname TEXT NOT NULL,
			ip TEXT NOT NULL,
			port INTEGER NOT NULL,
			status TEXT NOT NULL,
			os TEXT NOT NULL DEFAULT '',
			arch TEXT NOT NULL DEFAULT '',
			agent_version TEXT NOT NULL DEFAULT '',
			first_seen TEXT NOT NULL,
			last_seen TEXT NOT NULL,
			tls INTEGER NOT NULL DEFAULT 0,
			token TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	// Migrate pre-tls databases: add the tls column (no-op when present).
	rows, err := s.db.Query(`PRAGMA table_info(nodes)`)
	if err != nil {
		return err
	}
	hasTLS := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "tls" {
			hasTLS = true
		}
	}
	rows.Close()
	if !hasTLS {
		if _, err := s.db.Exec(`ALTER TABLE nodes ADD COLUMN tls INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// --- users ---

// CreateUser inserts a user; returns ErrExists on duplicate username.
func (s *Store) CreateUser(username, hash string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		username, hash, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrExists
		}
		return 0, err
	}
	return res.LastInsertId()
}

// GetUserByUsername fetches a user by name.
func (s *Store) GetUserByUsername(username string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(
		`SELECT id, username, password_hash, created_at FROM users WHERE username = ?`,
		username).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// ListUsers returns all users.
func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT id, username, password_hash, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser removes a user by id.
func (s *Store) DeleteUser(id int64) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUserPassword sets a new password hash.
func (s *Store) UpdateUserPassword(id int64, hash string) error {
	res, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountUsers returns the number of users (used for bootstrap).
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// --- nodes ---

// UpsertNode inserts or updates a node, keyed by UUID. An existing token and
// status are preserved on conflict: discovery must never clobber an
// operator-set token nor de-approve an already approved node.
func (s *Store) UpsertNode(n *Node) error {
	_, err := s.db.Exec(`INSERT INTO nodes (id, hostname, ip, port, status, os, arch, agent_version, first_seen, last_seen, tls, token)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname = excluded.hostname,
			ip = excluded.ip,
			port = excluded.port,
			os = excluded.os,
			arch = excluded.arch,
			agent_version = excluded.agent_version,
			last_seen = excluded.last_seen,
			tls = excluded.tls,
			status = nodes.status,
			token = nodes.token`,
		n.ID, n.Hostname, n.IP, n.Port, n.Status, n.OS, n.Arch, n.AgentVersion, n.FirstSeen, n.LastSeen, n.TLS, n.Token)
	return err
}

// SetNodeToken stores the agent token for a node (operator pairing).
func (s *Store) SetNodeToken(id, token string) error {
	res, err := s.db.Exec(`UPDATE nodes SET token = ? WHERE id = ?`, token, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetNode fetches a node by UUID.
func (s *Store) GetNode(id string) (*Node, error) {
	n := &Node{}
	err := s.db.QueryRow(
		`SELECT id, hostname, ip, port, status, os, arch, agent_version, first_seen, last_seen, tls, token
		 FROM nodes WHERE id = ?`, id).
		Scan(&n.ID, &n.Hostname, &n.IP, &n.Port, &n.Status, &n.OS, &n.Arch, &n.AgentVersion, &n.FirstSeen, &n.LastSeen, &n.TLS, &n.Token)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return n, nil
}

// ListNodes returns all nodes ordered by last_seen desc.
func (s *Store) ListNodes() ([]Node, error) {
	rows, err := s.db.Query(`SELECT id, hostname, ip, port, status, os, arch, agent_version, first_seen, last_seen, tls, token
		FROM nodes ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Hostname, &n.IP, &n.Port, &n.Status, &n.OS, &n.Arch, &n.AgentVersion, &n.FirstSeen, &n.LastSeen, &n.TLS, &n.Token); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SetNodeStatus updates a node's status (and last_seen if online).
func (s *Store) SetNodeStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE nodes SET status = ?, last_seen = ? WHERE id = ?`,
		status, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// DeleteNode removes a node by UUID.
func (s *Store) DeleteNode(id string) error {
	res, err := s.db.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- settings ---

// GetSetting reads a setting value; returns "" when absent.
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting writes a setting value.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ErrExists / ErrNotFound are sentinel store errors.
var (
	ErrExists   = errors.New("record exists")
	ErrNotFound = errors.New("not found")
)

func isUniqueViolation(err error) bool {
	// modernc sqlite reports "constraint failed: UNIQUE constraint failed: ..."
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// RandomToken returns a hex string of n random bytes.
func RandomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
