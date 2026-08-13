package config

import (
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
	Ufw               UfwConfig `yaml:"ufw"`
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
	if c.Ufw.BinaryPath == "" {
		c.Ufw.BinaryPath = "/usr/sbin/ufw"
	}
	if c.Token == "" {
		return nil, os.ErrInvalid
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return nil, os.ErrInvalid
	}
	return &c, nil
}
