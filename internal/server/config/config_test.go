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
	if c.ListenAddr != ":8080" || c.DBPath != "gopit.db" || c.TLSSkipVerify ||
		c.DiscoveryBroadcast != "255.255.255.255" || c.DiscoveryPort != 1221 || c.RateLimitPerMin != 100 {
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

func TestValidateFailsFast(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"empty db_path", "db_path: \"\n"},
		{"empty listen_addr", "listen_addr: \"\n"},
		{"bad port", "discovery_port: 99999\n"},
		{"zero rate limit", "rate_limit_per_min: 0\n"},
		{"conflicting secrets", "jwt_secret: a\njwt_secret_file: /tmp/s\n"},
		{"password without username", "admin_password: pw\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "s.yaml")
			os.WriteFile(p, []byte(tc.yaml), 0o600)
			if _, err := Load(p); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestMaintenanceDefault(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DBMaintenanceHours != 168 {
		t.Fatalf("maintenance default: %d", c.DBMaintenanceHours)
	}
}
