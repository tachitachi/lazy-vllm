package main

import (
	"testing"

	"lazy-vllm-proxy/internal/config"
)

func TestLoadConfig(t *testing.T) {
	t.Setenv("BACKENDS_MAP", `[{"prefix":"a","url":"http://a"},{"prefix":"b","url":"http://b"}]`)
	t.Setenv("PORT", "9090")

	cfg := config.LoadConfig()
	if cfg.Port != 9090 {
		t.Errorf("port = %d, want 9090", cfg.Port)
	}
	if len(cfg.Backends) != 2 {
		t.Errorf("backends = %d, want 2", len(cfg.Backends))
	}
	if cfg.Backends[0].URL != "http://a" {
		t.Errorf("first backend URL = %q, want %q", cfg.Backends[0].URL, "http://a")
	}
}
