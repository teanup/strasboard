package main

import (
	"os"
	"strconv"
)

type Config struct {
	Port string

	WeatherAPIURL    string
	WeatherLatitude  float64
	WeatherLongitude float64
	WeatherTimezone  string

	TransportAPIURL string
	TransportAPIKey string
	TransportStops  string

	ElectricityAPIURL   string
	ElectricityClientID string
	ElectricityUsername string
	ElectricityPassword string

	TempoAPIURL    string
	TempoAuthURL   string
	TempoAuthToken string
}

// Read environment variables
func LoadConfig() *Config {
	return &Config{
		Port: getEnv("PORT", "80"),

		WeatherAPIURL:    getEnv("WEATHER_API_URL", ""),
		WeatherLatitude:  getEnvFloat("WEATHER_LATITUDE", 48.58),
		WeatherLongitude: getEnvFloat("WEATHER_LONGITUDE", 7.75),
		WeatherTimezone:  getEnv("WEATHER_TIMEZONE", "Europe/Paris"),

		TransportAPIURL: getEnv("TRANSPORT_API_URL", ""),
		TransportAPIKey: getEnv("TRANSPORT_API_KEY", ""),
		TransportStops:  getEnv("TRANSPORT_STOPS", ""),

		ElectricityAPIURL:   getEnv("ELECTRICITY_API_URL", ""),
		ElectricityClientID: getEnv("ELECTRICITY_CLIENT_ID", ""),
		ElectricityUsername: getEnv("ELECTRICITY_USERNAME", ""),
		ElectricityPassword: getEnv("ELECTRICITY_PASSWORD", ""),

		TempoAPIURL:    getEnv("TEMPO_API_URL", ""),
		TempoAuthURL:   getEnv("TEMPO_AUTH_URL", ""),
		TempoAuthToken: getEnv("TEMPO_AUTH_TOKEN", ""),
	}
}

// Get a string env variable
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Get a float env variable
func getEnvFloat(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			return f
		}
	}
	return defaultValue
}
