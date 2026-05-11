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

func TestLoadConfig(t *testing.T) {
	t.Run("missing BACKENDS_MAP", func(t *testing.T) {
		_, err := config.LoadConfig()
		if err == nil {
			t.Error("expected error for missing BACKENDS_MAP")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		t.Setenv("BACKENDS_MAP", "not-json")
		_, err := config.LoadConfig()
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
	})

	t.Run("valid config", func(t *testing.T) {
		t.Setenv("BACKENDS_MAP", `[{"prefix":"gpt-4","url":"http://localhost:8001"},{"prefix":"claude-","url":"http://localhost:8002"}]`)
		t.Setenv("PORT", "9999")
		t.Setenv("LOG_DIR", "/tmp/logs")
		cfg, err := config.LoadConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg.Backends) != 2 {
			t.Errorf("expected 2 backends, got %d", len(cfg.Backends))
		}
		if cfg.Port != 9999 {
			t.Errorf("Port = %d, want 9999", cfg.Port)
		}
		if cfg.LogDir != "/tmp/logs" {
			t.Errorf("LogDir = %q, want /tmp/logs", cfg.LogDir)
		}
	})
}
