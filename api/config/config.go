// Package config loads the API configuration from a JSON file.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Config is the root configuration.
type Config struct {
	CORS        CORS        `json:"cors"`
	Snapshot    Snapshot    `json:"snapshot"`
	Weather     Weather     `json:"weather"`
	Transport   Transport   `json:"transport"`
	Electricity Electricity `json:"electricity"`
	Tempo       Tempo       `json:"tempo"`
}

// CORS configures cross-origin access for the separately hosted front-end.
type CORS struct {
	AllowedOrigins []string `json:"allowed_origins"`
}

// Snapshot configures persistence of the store.
type Snapshot struct {
	Path            string `json:"path"`
	IntervalMinutes int    `json:"interval_minutes"`
}

// Weather configures the Open-Meteo source.
type Weather struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
}

// Transport configures the CTS source.
type Transport struct {
	APIKey string `json:"api_key"`
	Stops  string `json:"stops"`
}

// Electricity configures the SER source.
type Electricity struct {
	ClientID string `json:"client_id"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Tempo configures the RTE Tempo source.
type Tempo struct {
	AuthToken string `json:"auth_token"`
}

// SnapshotInterval returns the snapshot interval as a duration.
func (c *Config) SnapshotInterval() time.Duration {
	return time.Duration(c.Snapshot.IntervalMinutes) * time.Minute
}

// Load reads the configuration file and validates required fields.
func Load() (*Config, error) {
	path := os.Getenv("CONFIG_PATH")
	if path == "" {
		path = "/etc/strasboard/api.json"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if cfg.Snapshot.Path == "" {
		return nil, fmt.Errorf("snapshot.path not set")
	}
	if cfg.Snapshot.IntervalMinutes <= 0 {
		return nil, fmt.Errorf("snapshot.interval_minutes not set")
	}
	if cfg.Weather.Timezone == "" {
		return nil, fmt.Errorf("weather.timezone not set")
	}
	return &cfg, nil
}
