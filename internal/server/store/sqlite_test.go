package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUserCRUD(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateUser("alice", "hash1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("alice", "hash2"); !errors.Is(err, ErrExists) {
		t.Fatalf("expected ErrExists, got %v", err)
	}
	u, err := s.GetUserByUsername("alice")
	if err != nil || u.PasswordHash != "hash1" || u.ID != id {
		t.Fatalf("get user failed: %v %+v", err, u)
	}
	users, err := s.ListUsers()
	if err != nil || len(users) != 1 {
		t.Fatalf("list users: %v %d", err, len(users))
	}
	if err := s.UpdateUserPassword(id, "hash2"); err != nil {
		t.Fatal(err)
	}
	u, _ = s.GetUserByUsername("alice")
	if u.PasswordHash != "hash2" {
		t.Fatal("password not updated")
	}
	if err := s.DeleteUser(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUserByUsername("alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestNodeCRUD(t *testing.T) {
	s := newTestStore(t)
	n := &Node{ID: "uuid-1", Hostname: "box1", IP: "10.0.0.1", Port: 1221,
		Status: StatusPending, OS: "linux", Arch: "amd64", AgentVersion: "dev",
		FirstSeen: "now", LastSeen: "now", TLS: true, Token: "tok"}
	if err := s.UpsertNode(n); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNode(n); err != nil { // upsert must not error on duplicate
		t.Fatal(err)
	}
	got, err := s.GetNode("uuid-1")
	if err != nil || got.Hostname != "box1" || got.Token != "tok" || !got.TLS {
		t.Fatalf("get node: %v %+v", err, got)
	}
	if err := s.SetNodeStatus("uuid-1", StatusOnline); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetNode("uuid-1")
	if got.Status != StatusOnline {
		t.Fatal("status not updated")
	}
	nodes, err := s.ListNodes()
	if err != nil || len(nodes) != 1 {
		t.Fatalf("list nodes: %v %d", err, len(nodes))
	}
	if err := s.DeleteNode("uuid-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNode("uuid-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSettings(t *testing.T) {
	s := newTestStore(t)
	if v, _ := s.GetSetting("k"); v != "" {
		t.Fatal("expected empty default")
	}
	if err := s.SetSetting("k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("k", "v2"); err != nil {
		t.Fatal(err)
	}
	v, err := s.GetSetting("k")
	if err != nil || v != "v2" {
		t.Fatalf("get setting: %v %q", err, v)
	}
	if len(RandomToken(16)) != 32 {
		t.Fatal("RandomToken length mismatch")
	}
}
