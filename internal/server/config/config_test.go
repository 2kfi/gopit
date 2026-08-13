package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8080" || c.DBPath != "gopit.db" || !c.TLSSkipVerify ||
		c.DiscoveryBroadcast != "255.255.255.255" || c.DiscoveryPort != 1221 {
		t.Fatalf("defaults not applied: %+v", c)
	}
}

func TestLoadOverrides(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.yaml")
	os.WriteFile(p, []byte("listen_addr: :9090\ndb_path: /tmp/x.db\n"), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":9090" || c.DBPath != "/tmp/x.db" {
		t.Fatalf("overrides not applied: %+v", c)
	}
}
