package config

import (
	"encoding/json"
	"fmt"
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
	Name        string `json:"name"`
	URL         string `json:"url"`
	CountTokens bool   `json:"count_tokens"`
}

// RouteRule maps one ROUTING_RULES entry.
type RouteRule struct {
	SourceModel string `json:"source_model"`
	Threshold   int    `json:"threshold"`
	TargetModel string `json:"target_model"`
}

// Config holds the proxy's configuration parsed from environment variables.
type Config struct {
	Backends        []Backend
	RoutingRules    []RouteRule
	Port            int
	LogDir          string
	MemoryIngestURL string
}

// LoadConfig reads configuration from environment variables.
// Returns an error if BACKENDS_MAP is not set or is invalid JSON.
func LoadConfig() (*Config, error) {
	raw := EnvOr("BACKENDS_MAP", "")
	if raw == "" {
		return nil, fmt.Errorf("BACKENDS_MAP is required")
	}

	var backends []Backend
	if err := json.Unmarshal([]byte(raw), &backends); err != nil {
		return nil, fmt.Errorf("invalid BACKENDS_MAP: %w", err)
	}

	var routingRules []RouteRule
	if r := EnvOr("ROUTING_RULES", ""); r != "" {
		if err := json.Unmarshal([]byte(r), &routingRules); err != nil {
			return nil, fmt.Errorf("invalid ROUTING_RULES: %w", err)
		}
	}

	return &Config{
		Backends:        backends,
		RoutingRules:    routingRules,
		Port:            EnvIntOr("PORT", 8002),
		LogDir:          EnvOr("LOG_DIR", ""),
		MemoryIngestURL: EnvOr("MEMORY_INGEST_URL", ""),
	}, nil
}
