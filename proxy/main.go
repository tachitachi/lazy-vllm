package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type backendRule struct {
	Prefix string `json:"prefix"`
	URL    string `json:"url"`
}

type server struct {
	backends   []backendRule
	levelVar   *slog.LevelVar
	diskLogger *DiskLogger
}

func resolveBackend(backends []backendRule, model string) string {
	for _, r := range backends {
		if strings.HasPrefix(model, r.Prefix) {
			return r.URL
		}
	}
	if len(backends) > 0 {
		return backends[0].URL
	}
	return ""
}

func extractModel(body []byte) string {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return ""
	}
	if m, ok := top["model"]; ok {
		var model string
		if err := json.Unmarshal(m, &model); err == nil {
			return model
		}
	}
	return ""
}

var passthroughHeaders = []string{
	"Authorization", "x-api-key", "Content-Type", "Accept",
	"anthropic-version", "anthropic-beta",
}

func forwardRequest(ctx context.Context, w http.ResponseWriter, r *http.Request, baseURL string, body []byte) {
	upstream, err := http.NewRequestWithContext(ctx, r.Method,
		baseURL+r.RequestURI, io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		http.Error(w, "failed to build upstream request", http.StatusInternalServerError)
		return
	}
	for _, h := range passthroughHeaders {
		if v := r.Header.Get(h); v != "" {
			upstream.Header.Set(h, v)
		}
	}
	resp, err := http.DefaultClient.Do(upstream)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (s *server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	model := extractModel(body)
	baseURL := resolveBackend(s.backends, model)

	ctx := r.Context()
	if s.diskLogger != nil {
		reqLog := s.diskLogger.Start("openai", r.URL.Path, r.Header, body)
		ctx = withReqLog(ctx, reqLog)
		defer s.diskLogger.Save(reqLog)
	}

	forwardRequest(ctx, w, r, baseURL, body)
}

func (s *server) handleMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	model := extractModel(body)
	baseURL := resolveBackend(s.backends, model)

	ctx := r.Context()
	if s.diskLogger != nil {
		reqLog := s.diskLogger.Start("anthropic", r.URL.Path, r.Header, body)
		ctx = withReqLog(ctx, reqLog)
		defer s.diskLogger.Save(reqLog)
	}

	forwardRequest(ctx, w, r, baseURL, body)
}

func (s *server) handleGenericProxy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	model := extractModel(body)
	baseURL := resolveBackend(s.backends, model)

	upstream, err := http.NewRequestWithContext(r.Context(), r.Method,
		baseURL+r.RequestURI, io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		http.Error(w, "failed to build upstream request", http.StatusInternalServerError)
		return
	}
	for _, h := range []string{"Authorization", "Content-Type", "Accept"} {
		if v := r.Header.Get(h); v != "" {
			upstream.Header.Set(h, v)
		}
	}
	resp, err := http.DefaultClient.Do(upstream)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (s *server) handleLogLevel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	var level slog.Level
	switch req.Level {
	case "DEBUG":
		level = slog.LevelDebug
	case "INFO":
		level = slog.LevelInfo
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		http.Error(w, "invalid log level", http.StatusBadRequest)
		return
	}
	s.levelVar.Set(level)
	slog.Info("log level changed", "new_level", level.String())
	w.WriteHeader(http.StatusOK)
}

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

func parseLogLevel(s string) slog.Level {
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

func main() {
	var backends []backendRule
	if raw := envOr("BACKENDS_MAP", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &backends); err != nil {
			slog.Error("invalid BACKENDS_MAP", "err", err)
			os.Exit(1)
		}
		slog.Info("loaded backends", "rules", len(backends))
	} else {
		slog.Error("BACKENDS_MAP is required")
		os.Exit(1)
	}

	port := envIntOr("PORT", 8002)

	levelVar := &slog.LevelVar{}
	levelVar.Set(parseLogLevel(envOr("LOG_LEVEL", "INFO")))
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: levelVar})))

	slog.Info("starting lazy-vllm-proxy", "port", port)

	var diskLogger *DiskLogger
	if logDir := envOr("LOG_DIR", ""); logDir != "" {
		var err error
		diskLogger, err = NewDiskLogger(logDir)
		if err != nil {
			slog.Warn("disk logger init failed", "err", err)
		} else {
			slog.Info("disk logging enabled", "log_dir", logDir)
		}
	}

	s := &server{backends: backends, levelVar: levelVar, diskLogger: diskLogger}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/messages", s.handleMessages)
	mux.HandleFunc("POST /log-level", s.handleLogLevel)
	if diskLogger != nil {
		mux.HandleFunc("GET /ui/logs", s.handleLogsUI)
		mux.HandleFunc("GET /api/logs", s.handleLogsList)
		mux.HandleFunc("GET /api/logs/{id}", s.handleLogDetail)
	}
	mux.HandleFunc("/", s.handleGenericProxy)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
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
