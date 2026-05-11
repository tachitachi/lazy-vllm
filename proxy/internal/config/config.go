package config

import (
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
)

// EnvOr returns the value of the named env var, or fallback if empty.
func EnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// EnvIntOr returns the int value of the named env var, or fallback if empty/invalid.
func EnvIntOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// ParseLogLevel returns the slog.Level for a log level string.
func ParseLogLevel(s string) slog.Level {
	switch s {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Backend maps one BACKENDS_MAP entry.
type Backend struct {
	Prefix string `json:"prefix"`
	URL    string `json:"url"`
}

// Config holds the proxy's configuration parsed from environment variables.
type Config struct {
	Backends []Backend
	Port     int
	LogDir   string
}

// LoadConfig reads configuration from environment variables.
// Exits if BACKENDS_MAP is not set or is invalid JSON.
func LoadConfig() *Config {
	raw := EnvOr("BACKENDS_MAP", "")
	if raw == "" {
		slog.Error("BACKENDS_MAP is required")
		os.Exit(1)
	}

	var backends []Backend
	if err := json.Unmarshal([]byte(raw), &backends); err != nil {
		slog.Error("invalid BACKENDS_MAP", "err", err)
		os.Exit(1)
	}
	slog.Info("loaded backends", "rules", len(backends))

	return &Config{
		Backends: backends,
		Port:     EnvIntOr("PORT", 8002),
		LogDir:   EnvOr("LOG_DIR", ""),
	}
}
