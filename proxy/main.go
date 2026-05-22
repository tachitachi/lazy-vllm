package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lazy-vllm-proxy/internal/config"
	"lazy-vllm-proxy/internal/logger"
	"lazy-vllm-proxy/internal/proxy"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	thinkingBudget := config.EnvFloatOr("THINKING_BUDGET", 0.9)
	slog.Info("loaded backends", "backends", len(cfg.Backends), "rules", len(cfg.RoutingRules), "thinking_budget", thinkingBudget)

	levelVar := &slog.LevelVar{}
	levelVar.Set(config.ParseLogLevel(config.EnvOr("LOG_LEVEL", "INFO")))

	s := &proxy.Server{
		Backends:               cfg.Backends,
		Providers:              cfg.Providers,
		RoutingRules:           cfg.RoutingRules,
		LevelVar:               levelVar,
		MemoryIngestURL:        cfg.MemoryIngestURL,
		ThinkingBudgetFraction: thinkingBudget,
	}

	var compactLogger *logger.CompactLogger
	if cfg.LogDir != "" {
		var err error
		compactLogger, err = logger.NewCompact(cfg.LogDir)
		if err != nil {
			slog.Warn("compact logger init failed", "err", err)
		} else {
			slog.Info("compact logging enabled", "log_dir", cfg.LogDir)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		s.HandleChatCompletions(w, r, body, compactLogger)
	})
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		s.HandleMessages(w, r, body, compactLogger)
	})
	mux.HandleFunc("GET /v1/models", s.HandleModels)
	mux.HandleFunc("POST /log-level", s.HandleLogLevel)
	if compactLogger != nil {
		mux.HandleFunc("GET /ui/logs", compactLogger.HandleLogsUI)
		mux.HandleFunc("GET /api/sessions", compactLogger.HandleSessionsList)
		mux.HandleFunc("GET /api/sessions/{id}", compactLogger.HandleSessionDetail)
		mux.HandleFunc("GET /api/sessions/{id}/raw", compactLogger.HandleRawRequest)
		mux.HandleFunc("GET /api/tools/{hash}", compactLogger.HandleToolsDetail)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		s.HandleGenericProxy(w, r, body)
	})

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      proxy.AttributionMiddleware(cfg.Providers)(mux),
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
	_ = httpServer.Shutdown(ctx)
}
