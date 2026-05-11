package config_test

import (
	"log/slog"
	"testing"

	"lazy-vllm-proxy/internal/config"
)

func TestEnvOr(t *testing.T) {
	t.Setenv("TEST_VAR", "hello")

	tests := []struct {
		name     string
		key      string
		fallback string
		want     string
	}{
		{"existing", "TEST_VAR", "default", "hello"},
		{"missing", "NONEXISTENT_VAR", "default", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := config.EnvOr(tt.key, tt.fallback)
			if got != tt.want {
				t.Errorf("EnvOr(%q, %q) = %q, want %q", tt.key, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestEnvIntOr(t *testing.T) {
	t.Setenv("TEST_PORT_EXISTING", "9999")

	tests := []struct {
		name     string
		key      string
		fallback int
		want     int
	}{
		{"existing", "TEST_PORT_EXISTING", 8080, 9999},
		{"missing", "NONEXISTENT_PORT", 443, 443},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := config.EnvIntOr(tt.key, tt.fallback)
			if got != tt.want {
				t.Errorf("EnvIntOr(%q, %d) = %d, want %d", tt.key, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestEnvIntOr_Invalid(t *testing.T) {
	t.Setenv("TEST_PORT_INVALID", "not-a-number")
	got := config.EnvIntOr("TEST_PORT_INVALID", 123)
	if got != 123 {
		t.Errorf("EnvIntOr with invalid value = %d, want 123", got)
	}
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"DEBUG", slog.LevelDebug},
		{"INFO", slog.LevelInfo},
		{"WARN", slog.LevelWarn},
		{"WARNING", slog.LevelWarn},
		{"ERROR", slog.LevelError},
		{"", slog.LevelInfo},
		{"INVALID", slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := config.ParseLogLevel(tt.input)
			if got != tt.want {
				t.Errorf("ParseLogLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
