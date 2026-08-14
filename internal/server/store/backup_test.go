package store

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	dst := filepath.Join(dir, "dst.db")

	s, err := Open(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("alice", "hash1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("bob", "hash2"); err != nil {
		t.Fatal(err)
	}
	tricky := `token with 'quote' ;semicolon; and
newline`
	if err := s.UpsertNode(&Node{ID: "uuid-1", Hostname: "box1", IP: "10.0.0.1", Port: 1221,
		Status: StatusPending, OS: "linux", Arch: "amd64", FirstSeen: "now", LastSeen: "now", Token: tricky}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("jwt_secret", tricky+"x"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	var buf bytes.Buffer
	if err := Dump(src, &buf); err != nil {
		t.Fatal(err)
	}
	if err := Restore(dst, &buf); err != nil {
		t.Fatal(err)
	}

	d, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if n, _ := d.CountUsers(); n != 2 {
		t.Fatalf("users after restore: %d", n)
	}
	if n, _ := d.CountNodes(); n != 1 {
		t.Fatalf("nodes after restore: %d", n)
	}
	n, err := d.GetNode("uuid-1")
	if err != nil || n.Token != tricky {
		t.Fatalf("node data after restore: %v %q", err, n.Token)
	}
	if v, _ := d.GetSetting("jwt_secret"); v != tricky+"x" {
		t.Fatalf("setting after restore: %q", v)
	}
}

func TestRestoreBrokenDump(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "keep.db")
	s, _ := Open(p)
	s.Close()

	if err := Restore(p, strings.NewReader(`INSERT INTO nope VALUES(1);`)); err == nil {
		t.Fatal("expected error restoring a broken dump")
	}
	if _, err := Open(p); err != nil {
		t.Fatalf("original db must survive a broken restore: %v", err)
	}
}
