package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for mqtt2ha.
type Config struct {
	MQTT    MQTTConfig `yaml:"mqtt"`
	Mode    string     `yaml:"mode"`          // "auto" or "approval"
	Observe int        `yaml:"observe_count"` // messages to observe before publishing in auto mode
	DBPath  string     `yaml:"database"`
	HTTP    string     `yaml:"http"` // web UI listen address, e.g. ":8080"
}

type MQTTConfig struct {
	Broker          string   `yaml:"broker"`
	Username        string   `yaml:"username"`
	Password        string   `yaml:"password"`
	Subscribe       []string `yaml:"subscribe"`        // data topic prefixes, e.g. ["#"]
	DiscoveryPrefix string   `yaml:"discovery_prefix"` // default "homeassistant"
	KeepAlive       int      `yaml:"keep_alive"`
	ClientID        string   `yaml:"client_id"`
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		MQTT: MQTTConfig{
			Broker:          "127.0.0.1:1883",
			Subscribe:       []string{"#"},
			DiscoveryPrefix: "homeassistant",
			KeepAlive:       30,
			ClientID:        "mqtt2ha",
		},
		Mode:    "auto",
		Observe: 3,
		DBPath:  "mqtt2ha.db",
		HTTP:    ":8080",
	}
}

// LoadConfig reads config from path, falling back to defaults for missing fields.
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Mode != "auto" && cfg.Mode != "approval" {
		return nil, fmt.Errorf("mode must be 'auto' or 'approval', got %q", cfg.Mode)
	}
	if cfg.Observe < 1 {
		cfg.Observe = 3
	}
	return cfg, nil
}
