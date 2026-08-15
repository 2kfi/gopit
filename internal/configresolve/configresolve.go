// Package configresolve locates the server/agent config file, writing a
// bundled default on first run so the binaries work out of the box.
package configresolve

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed gopit.yaml
var serverTemplate []byte

//go:embed gopitd.yaml
var agentTemplate []byte

// ResolveServer returns the server config path to use. An explicit -config
// wins; otherwise the first existing of ./configs/gopit.yaml and
// <userConfigDir>/gopit/gopit.yaml. When neither exists the bundled default
// is written to the user-level path (first run) and returned.
func ResolveServer(explicit string) (string, error) {
	return resolve("gopit.yaml", explicit, serverTemplate)
}

// ResolveAgent is ResolveServer for the node agent config.
func ResolveAgent(explicit string) (string, error) {
	return resolve("gopitd.yaml", explicit, agentTemplate)
}

func resolve(name, explicit string, tmpl []byte) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config file %s: %w", explicit, err)
		}
		return explicit, nil
	}
	if _, err := os.Stat(filepath.Join(".", "configs", name)); err == nil {
		return filepath.Join(".", "configs", name), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	path := filepath.Join(dir, "gopit", name)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := writeDefault(path, name, tmpl); err != nil {
		return "", fmt.Errorf("writing default config to %s: %w", path, err)
	}
	return path, nil
}

func writeDefault(path, name string, tmpl []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content := tmpl
	if name == "gopit.yaml" {
		content = []byte(strings.Replace(string(tmpl), "__RANDOM_PAIRING_TOKEN__", randomToken(), 1))
	}
	return os.WriteFile(path, content, 0o600)
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "change-me"
	}
	return hex.EncodeToString(b)
}
