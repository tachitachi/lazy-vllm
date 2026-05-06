package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"thinking-router/internal/router"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func main() {
	cfg := router.Config{
		VLLMBaseURL: envOr("VLLM_BASE_URL", "http://gemma4:8000"),
		Port:        envIntOr("PORT", 8001),
		WindowSize:  envIntOr("ROUTER_WINDOW_SIZE", 3),
		ModelName:   envOr("MODEL_NAME", "google/gemma-4-26B-A4B-it"),
	}

	levelVar := &slog.LevelVar{}
	levelVar.Set(slog.LevelInfo)

	// Use TextHandler for better readability in console
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: levelVar})
	slog.SetDefault(slog.New(handler))

	slog.Info("starting thinking-router",
		"vllm_base_url", cfg.VLLMBaseURL,
		"port", cfg.Port,
		"window_size", cfg.WindowSize,
		"model", cfg.ModelName,
	)

	s := router.NewServer(cfg, levelVar)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("POST /v1/chat/completions", s.HandleChatCompletions)
	mux.HandleFunc("POST /v1/messages", s.HandleMessages)
	mux.HandleFunc("POST /log-level", s.HandleLogLevel)
	mux.HandleFunc("/", s.HandleGenericProxy)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		slog.Info("listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-quit
	slog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpServer.Shutdown(ctx)
}
