package main

import (
	"log/slog"
	"testing"
)

func TestResolveBackend(t *testing.T) {
	backends := []backendRule{
		{Prefix: "gemma", URL: "http://localhost:8000"},
		{Prefix: "qwen", URL: "http://localhost:8001"},
		{Prefix: "llama", URL: "http://localhost:8002"},
	}

	tests := []struct {
		model string
		want  string
	}{
		{"gemma-3-4b", "http://localhost:8000"},
		{"gemma", "http://localhost:8000"},
		{"qwen-3-8b", "http://localhost:8001"},
		{"llama-3-70b", "http://localhost:8002"},
		{"unknown-model", "http://localhost:8000"},
		{"", "http://localhost:8000"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := resolveBackend(backends, tt.model)
			if got != tt.want {
				t.Errorf("resolveBackend(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestResolveBackend_Fallback(t *testing.T) {
	// No prefix matches — falls back to first backend.
	backends := []backendRule{
		{Prefix: "gemma", URL: "http://localhost:8000"},
	}
	got := resolveBackend(backends, "anything")
	if got != "http://localhost:8000" {
		t.Errorf("resolveBackend(_) = %q, want first backend", got)
	}
}

func TestResolveBackend_Empty(t *testing.T) {
	got := resolveBackend(nil, "anything")
	if got != "" {
		t.Errorf("resolveBackend(nil, _) = %q, want empty", got)
	}
}

func TestUniqueURLs(t *testing.T) {
	tests := []struct {
		name  string
		input []backendRule
		want  []string
	}{
		{
			name: "all unique",
			input: []backendRule{
				{Prefix: "a", URL: "http://srv1"},
				{Prefix: "b", URL: "http://srv2"},
				{Prefix: "c", URL: "http://srv3"},
			},
			want: []string{"http://srv1", "http://srv2", "http://srv3"},
		},
		{
			name: "duplicates",
			input: []backendRule{
				{Prefix: "a", URL: "http://srv1"},
				{Prefix: "b", URL: "http://srv2"},
				{Prefix: "c", URL: "http://srv1"},
			},
			want: []string{"http://srv1", "http://srv2"},
		},
		{
			name:  "empty",
			input: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := uniqueURLs(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("uniqueURLs() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("uniqueURLs()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExtractModel(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{
			name: "openai format",
			body: []byte(`{"model":"gemma-3-4b","messages":[],"stream":false}`),
			want: "gemma-3-4b",
		},
		{
			name: "openai stream",
			body: []byte(`{"model":"qwen-3-8b","messages":[],"stream":true}`),
			want: "qwen-3-8b",
		},
		{
			name: "missing model",
			body: []byte(`{"messages":[]}`),
			want: "",
		},
		{
			name: "invalid json",
			body: []byte(`not json`),
			want: "",
		},
		{
			name: "empty body",
			body: []byte(``),
			want: "",
		},
		{
			name: "model is empty string",
			body: []byte(`{"model":""}`),
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractModel(tt.body)
			if got != tt.want {
				t.Errorf("extractModel(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

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
			got := envOr(tt.key, tt.fallback)
			if got != tt.want {
				t.Errorf("envOr(%q, %q) = %q, want %q", tt.key, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestEnvIntOr(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		fallback int
		want     int
	}{
		{"existing", "TEST_PORT_EXISTING", 8080, 9999},
		{"missing", "NONEXISTENT_PORT", 443, 443},
	}

	t.Setenv("TEST_PORT_EXISTING", "9999")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := envIntOr(tt.key, tt.fallback)
			if got != tt.want {
				t.Errorf("envIntOr(%q, %d) = %d, want %d", tt.key, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestEnvIntOr_Invalid(t *testing.T) {
	t.Setenv("TEST_PORT_INVALID", "not-a-number")
	got := envIntOr("TEST_PORT_INVALID", 123)
	if got != 123 {
		t.Errorf("envIntOr with invalid value = %d, want 123", got)
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
			got := parseLogLevel(tt.input)
			if got != tt.want {
				t.Errorf("parseLogLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
