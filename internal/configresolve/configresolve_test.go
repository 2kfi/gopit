package configresolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplicitConfigWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	if err := os.WriteFile(path, []byte("listen_addr: \":9999\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveServer(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("expected %s, got %s", path, got)
	}
}

func TestExplicitConfigMustExist(t *testing.T) {
	if _, err := ResolveServer(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing explicit config")
	}
}

func TestProjectConfigDirPreferred(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configs", "gopit.yaml"), []byte("listen_addr: \":9999\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got, err := ResolveServer("")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(".", "configs", "gopit.yaml") {
		t.Fatalf("expected ./configs/gopit.yaml, got %s", got)
	}
}

func TestFirstRunWritesUserDefault(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	got, err := ResolveServer("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cfgHome, "gopit", "gopit.yaml")
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
	b, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "__RANDOM_PAIRING_TOKEN__") {
		t.Fatal("server default must have a concrete pairing_token after first-run write")
	}
	fi, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("default config must be 0600, got %o", fi.Mode().Perm())
	}
}

func TestFirstRunWritesAgentDefault(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	got, err := ResolveAgent("")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(cfgHome, "gopit", "gopitd.yaml") {
		t.Fatalf("unexpected path %s", got)
	}
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "token: change-me") {
		t.Fatal("agent default should contain the change-me token template")
	}
}

func TestExistingUserConfigUsedNotOverwritten(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	dir := filepath.Join(cfgHome, "gopit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "gopit.yaml")
	if err := os.WriteFile(p, []byte("custom: content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveServer("")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(got)
	if !strings.Contains(string(b), "custom: content") {
		t.Fatal("existing user config must not be overwritten")
	}
}
