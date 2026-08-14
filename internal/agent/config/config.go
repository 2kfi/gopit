package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the agent's YAML configuration.
type Config struct {
	ListenAddr        string    `yaml:"listen_addr"` // e.g. 0.0.0.0
	Port              int       `yaml:"port"`        // TCP WS + UDP beacon, default 1221
	Token             string    `yaml:"token"`       // pre-shared pairing token
	UUIDPath          string    `yaml:"uuid_path"`   // file to read/persist node UUID
	TLSCert           string    `yaml:"tls_cert"`    // optional; empty = plaintext WS
	TLSKey            string    `yaml:"tls_key"`
	StatsIntervalSecs int       `yaml:"stats_interval_seconds"`
	Firewall          string    `yaml:"firewall"` // nftfw (default, no sudo) | ufw (legacy sudo integration)
	Ufw               UfwConfig `yaml:"ufw"`
	Terminal          TermConf  `yaml:"terminal"` // optional PTY session recording
}

// TermConf scopes the optional terminal session recording.
type TermConf struct {
	Record       bool   `yaml:"record"`        // write ttyrec files for every session; default off
	RecordingDir string `yaml:"recording_dir"` // base dir; sessions land in <dir>/<node_id>/<ts>.ttyrec
}

// UfwConfig scopes the ufw firewall integration.
type UfwConfig struct {
	BinaryPath  string `yaml:"binary_path"`  // default /usr/sbin/ufw
	AllowToggle bool   `yaml:"allow_toggle"` // permit ufw enable/disable; default off
}

// Load reads and validates the YAML file at path, applying defaults.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Port == 0 {
		c.Port = 1221
	}
	if c.ListenAddr == "" {
		c.ListenAddr = "0.0.0.0"
	}
	if c.UUIDPath == "" {
		c.UUIDPath = "/etc/gopitd/node.id"
	}
	if c.StatsIntervalSecs <= 0 {
		c.StatsIntervalSecs = 1
	}
	if c.Firewall == "" {
		c.Firewall = "nftfw"
	}
	if c.Firewall != "nftfw" && c.Firewall != "ufw" {
		return nil, fmt.Errorf("config %s: firewall must be \"nftfw\" or \"ufw\", got %q", path, c.Firewall)
	}
	if c.Ufw.BinaryPath == "" {
		c.Ufw.BinaryPath = "/usr/sbin/ufw"
	}
	if c.Terminal.RecordingDir == "" {
		c.Terminal.RecordingDir = "/var/lib/gopitd/recordings"
	}
	if c.Token == "" {
		return nil, fmt.Errorf("config %s: token is required (pre-shared pairing token; set TOKEN=... when installing via install.sh)", path)
	}
	if c.Port < 1 || c.Port > 65535 {
		return nil, fmt.Errorf("config %s: port must be 1-65535, got %d", path, c.Port)
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return nil, fmt.Errorf("config %s: tls_cert and tls_key must be set together (or both omitted for plain ws)", path)
	}
	return &c, nil
}
