package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agent.yaml")
	os.WriteFile(p, []byte("token: test-token\n"), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 1221 || c.ListenAddr != "0.0.0.0" || c.UUIDPath != "/etc/gopitd/node.id" || c.StatsIntervalSecs != 1 {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if _, err := uuid.Parse(c.Token); err == nil && c.Token != "" {
		t.Fatal("unexpected")
	}
}

func TestLoadMissingToken(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agent.yaml")
	os.WriteFile(p, []byte("port: 1221\n"), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for missing token")
	}
}
