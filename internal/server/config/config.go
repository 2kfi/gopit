package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the server's YAML configuration.
type Config struct {
	ListenAddr         string `yaml:"listen_addr"`
	DBPath             string `yaml:"db_path"`
	JWTSecret          string `yaml:"jwt_secret"`
	AdminUsername      string `yaml:"admin_username"`
	AdminPassword      string `yaml:"admin_password"`
	DiscoveryBroadcast string `yaml:"discovery_broadcast_addr"`
	DiscoveryPort      int    `yaml:"discovery_port"`
	TLSSkipVerify      bool   `yaml:"tls_skip_verify"`
	PairingToken       string `yaml:"pairing_token"` // shared with agents; operator pastes it per node at pair time
}

// Load reads the YAML file at path, applying defaults.
func Load(path string) (*Config, error) {
	c := Config{
		ListenAddr:         ":8080",
		DBPath:             "gopit.db",
		DiscoveryBroadcast: "255.255.255.255",
		DiscoveryPort:      1221,
		TLSSkipVerify:      true,
	}
	if b, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(b, &c); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return &c, nil
}
