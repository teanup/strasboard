// Package config loads the sensor configuration from a JSON file.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Config is the sensor runtime configuration.
type Config struct {
	Pin              string `json:"pin"`
	Location         string `json:"location"`
	ServerURL        string `json:"server_url"`
	AuthToken        string `json:"auth_token"`
	ReadIntervalSecs int    `json:"read_interval_seconds"`
	MaxRetries       int    `json:"max_retries"`
	Debug            bool   `json:"debug"`
}

// ReadInterval returns the read interval as a duration.
func (c *Config) ReadInterval() time.Duration {
	return time.Duration(c.ReadIntervalSecs) * time.Second
}

// Load reads the configuration file and validates required fields.
func Load() (*Config, error) {
	path := os.Getenv("CONFIG_PATH")
	if path == "" {
		path = "/etc/strasboard/sensor.json"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if cfg.Pin == "" {
		return nil, fmt.Errorf("pin not set")
	}
	if cfg.ServerURL == "" {
		return nil, fmt.Errorf("server_url not set")
	}
	if cfg.ReadIntervalSecs <= 0 {
		return nil, fmt.Errorf("read_interval_seconds not set")
	}
	if cfg.MaxRetries <= 0 {
		return nil, fmt.Errorf("max_retries not set")
	}
	return &cfg, nil
}
