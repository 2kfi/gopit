package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the server's YAML configuration.
type Config struct {
	ListenAddr         string        `yaml:"listen_addr"`
	DBPath             string        `yaml:"db_path"`
	JWTSecret          string        `yaml:"jwt_secret"`
	JWTSecretFile      string        `yaml:"jwt_secret_file"`
	AdminUsername      string        `yaml:"admin_username"`
	AdminPassword      string        `yaml:"admin_password"`
	AdminPasswordFile  string        `yaml:"admin_password_file"`
	DiscoveryBroadcast string        `yaml:"discovery_broadcast_addr"`
	DiscoveryPort      int           `yaml:"discovery_port"`
	TLSSkipVerify      bool          `yaml:"tls_skip_verify"`
	PairingToken       string        `yaml:"pairing_token"`
	PairingTokenFile   string        `yaml:"pairing_token_file"`
	RateLimitPerMin    int           `yaml:"rate_limit_per_min"`
	DBMaintenanceHours int           `yaml:"db_maintenance_interval"` // hours between WAL checkpoint+VACUUM; 0 disables
	PasswordMinScore   int           `yaml:"password_min_score"`      // minimum password strength 0-4 (zxcvbn), default 2
	Webhooks           []WebhookConf `yaml:"webhooks"`                // signed HTTP event callbacks; empty = disabled
}

// WebhookConf is one outgoing webhook endpoint.
type WebhookConf struct {
	URL    string   `yaml:"url"`
	Secret string   `yaml:"secret"`
	Events []string `yaml:"events"` // empty = all events
}

// Load reads the YAML file at path, applying defaults.
func Load(path string) (*Config, error) {
	c := Config{
		ListenAddr:         ":8080",
		DBPath:             "gopit.db",
		DiscoveryBroadcast: "255.255.255.255",
		DiscoveryPort:      1221,
		TLSSkipVerify:      false,
		RateLimitPerMin:    100,
		DBMaintenanceHours: 168,
		PasswordMinScore:   2,
	}
	if b, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(b, &c); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate fails fast on missing or conflicting required fields, with
// actionable messages. Optional fields keep their defaults.
func (c *Config) Validate() error {
	field := func(cond bool, name, hint string) error {
		if cond {
			return fmt.Errorf("config: %s: %s (set %s in the YAML, or remove the conflicting field)", name, hint, name)
		}
		return nil
	}
	if err := field(c.DBPath == "", "db_path", "required: SQLite database path, e.g. /var/lib/gopit/gopit.db"); err != nil {
		return err
	}
	if err := field(c.ListenAddr == "", "listen_addr", "required: HTTP listen address, e.g. :8080"); err != nil {
		return err
	}
	if err := field(c.DiscoveryPort < 1 || c.DiscoveryPort > 65535, "discovery_port", "must be a valid port (1-65535)"); err != nil {
		return err
	}
	if err := field(c.RateLimitPerMin <= 0, "rate_limit_per_min", "must be a positive number of requests per minute"); err != nil {
		return err
	}
	if err := field(c.JWTSecret != "" && c.JWTSecretFile != "", "jwt_secret/jwt_secret_file", "set only one; they conflict"); err != nil {
		return err
	}
	if err := field(c.PairingToken != "" && c.PairingTokenFile != "", "pairing_token/pairing_token_file", "set only one; they conflict"); err != nil {
		return err
	}
	if err := field(c.AdminUsername == "" && c.AdminPassword != "", "admin_username", "required when admin_password is set (bootstrap needs both)"); err != nil {
		return err
	}
	if err := field(c.AdminUsername != "" && c.AdminPassword == "" && c.AdminPasswordFile == "", "admin_password", "required when admin_username is set (bootstrap needs both)"); err != nil {
		return err
	}
	if err := field(c.DBMaintenanceHours < 0, "db_maintenance_interval", "must be >= 0 (0 disables maintenance)"); err != nil {
		return err
	}
	if err := field(c.PasswordMinScore < 0 || c.PasswordMinScore > 4, "password_min_score", "must be 0-4 (zxcvbn score)"); err != nil {
		return err
	}
	for i, h := range c.Webhooks {
		if h.URL == "" {
			continue // dropped by the webhook store
		}
		if err := webhookURL(h.URL); err != nil {
			return fmt.Errorf("config: webhooks[%d]: %s", i, err)
		}
		for _, e := range h.Events {
			if !knownEvent(e) {
				return fmt.Errorf("config: webhooks[%d]: unknown event %q (valid: node.up, node.down, firewall.changed, docker.deployed, user.login, user.created, user.deleted, password.changed)", i, e)
			}
		}
	}
	return nil
}

func webhookURL(u string) error {
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("invalid url: %s", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("invalid url: scheme must be http or https")
	}
	return nil
}

func knownEvent(e string) bool {
	for _, known := range []string{"node.up", "node.down", "firewall.changed", "docker.deployed", "user.login", "user.created", "user.deleted", "password.changed"} {
		if e == known {
			return true
		}
	}
	return false
}

// ResolveSecrets reads file-backed secrets (must be 0400 perms) and populates
// the primary fields. Call after Load before using the config.
func (c *Config) ResolveSecrets() error {
	if c.JWTSecretFile != "" {
		b, err := readSecretFile(c.JWTSecretFile)
		if err != nil {
			return err
		}
		c.JWTSecret = string(b)
	}
	if c.AdminPasswordFile != "" {
		b, err := readSecretFile(c.AdminPasswordFile)
		if err != nil {
			return err
		}
		c.AdminPassword = string(b)
	}
	if c.PairingTokenFile != "" {
		b, err := readSecretFile(c.PairingTokenFile)
		if err != nil {
			return err
		}
		c.PairingToken = string(b)
	}
	return nil
}

func readSecretFile(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	mode := fi.Mode().Perm()
	if mode&0o077 != 0 {
		return nil, &SecretFilePermError{Path: path, Perm: mode}
	}
	return os.ReadFile(path)
}

// SecretFilePermError is returned when a secret file has overly permissive permissions.
type SecretFilePermError struct {
	Path string
	Perm os.FileMode
}

func (e *SecretFilePermError) Error() string {
	return "secret file " + e.Path + " has permissions " + e.Perm.String() + ", must be 0400 (or 0600)"
}
