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
	"syscall"
	"time"

	"agent-graph/internal/agent"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const routingPrompt = "You are a query router. Classify the user's latest message as DIRECT or REASONING based on the cognitive depth required to provide an accurate response.\n\n" +
	"DIRECT: Use for shallow-processing tasks. This includes simple factual retrieval, greetings, or trivial transformations where the answer is immediate and requires no internal logical steps, synthesis of information, or complex reasoning.\n\n" +
	"REASONING: Use for deep-processing tasks. This includes queries that require:\n" +
	"- Multi-step logical deduction or sequential processing.\n" +
	"- Synthesis or abstraction (e.g., summarizing, identifying themes, or distilling information).\n" +
	"- Analytical reasoning (e.g., comparisons, critiques, or explaining 'why').\n" +
	"- Complex constraint satisfaction (e.g., following intricate formatting or stylistic rules).\n" +
	"- Processing high-density or structurally complex input.\n\n" +
	"Classify the message based on the required depth of processing."

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
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

func envIntOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func boolPtr(v bool) *bool { return &v }

// extractModel reads the "model" field from the raw request body (OpenAI or Anthropic format).
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

func buildDefaultGraph(backends []agent.BackendRule, cfg agent.Config) *agent.Graph {
	return &agent.Graph{
		Entry: "respond",
		Cfg:   cfg,
		Nodes: map[string]*agent.Node{
			"classify": {
				ID:   "classify",
				Kind: agent.NodeKindRoute,
				Agent: agent.Agent{
					SystemPrompt:     routingPrompt,
					MaxTokens:        16,
					ThinkingMode:     boolPtr(false),
					StructuredChoice: []string{"DIRECT", "REASONING"},
				},
			},
			"respond": {
				ID:   "respond",
				Kind: agent.NodeKindRespond,
			},
		},
		Edges: []agent.Edge{
			{
				From: "classify",
				To:   "respond",
				Transform: func(pctx *agent.PipelineCtx) {
					thinking := pctx.NodeOutputs["classify"] == "REASONING"
					pctx.ThinkingMode = &thinking
				},
			},
		},
	}
}

type server struct {
	graph      *agent.Graph
	cfg        agent.Config
	backends   []agent.BackendRule
	levelVar   *slog.LevelVar
	diskLogger *agent.DiskLogger
}

func (s *server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req struct {
		Messages []agent.ChatMessage `json:"messages"`
		Stream   bool                `json:"stream"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	model := extractModel(bodyBytes)
	pctx := &agent.PipelineCtx{
		OriginalBody:    bodyBytes,
		OriginalHeaders: r.Header.Clone(),
		Stream:          req.Stream,
		Format:          agent.FormatOpenAI,
		Messages:        agent.StripAllThinking(req.Messages),
		NodeOutputs:     make(map[string]string),
	}
	if model != "" {
		pctx.BackendURL = agent.ResolveBackend(s.backends, model)
	}

	ctx := r.Context()
	if s.diskLogger != nil {
		reqLog := s.diskLogger.Start("openai", r.URL.Path, r.Header, bodyBytes)
		ctx = agent.WithReqLog(ctx, reqLog)
		defer s.diskLogger.Save(reqLog)
	}

	if err := s.graph.Run(ctx, pctx, w); err != nil {
		slog.Error("graph execution failed", "err", err)
	}
}

func (s *server) handleMessages(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req struct {
		System   any                      `json:"system"`
		Messages []agent.AnthropicMessage `json:"messages"`
		Stream   bool                     `json:"stream"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	model := extractModel(bodyBytes)
	msgs := agent.AnthropicToChatMessages(req.System, req.Messages)

	pctx := &agent.PipelineCtx{
		OriginalBody:    bodyBytes,
		OriginalHeaders: r.Header.Clone(),
		Stream:          req.Stream,
		Format:          agent.FormatAnthropic,
		Messages:        agent.StripAllThinking(msgs),
		NodeOutputs:     make(map[string]string),
	}
	if model != "" {
		pctx.BackendURL = agent.ResolveBackend(s.backends, model)
	}

	ctx := r.Context()
	if s.diskLogger != nil {
		reqLog := s.diskLogger.Start("anthropic", r.URL.Path, r.Header, bodyBytes)
		ctx = agent.WithReqLog(ctx, reqLog)
		defer s.diskLogger.Save(reqLog)
	}

	if err := s.graph.Run(ctx, pctx, w); err != nil {
		slog.Error("graph execution failed", "err", err)
	}
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

func (s *server) handleGenericProxy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	model := extractModel(body)
	baseURL := agent.ResolveBackend(s.backends, model)

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

func main() {
	var backends []agent.BackendRule
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
	cfg := agent.Config{
		Port:       port,
		Backends:   backends,
		WindowSize: envIntOr("ROUTER_WINDOW_SIZE", 3),
		ModelName:  envOr("MODEL_NAME", "google/gemma-4-26B-A4B-it"),
	}

	levelVar := &slog.LevelVar{}
	levelVar.Set(parseLogLevel(envOr("LOG_LEVEL", "INFO")))
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: levelVar})))

	slog.Info("starting agent-graph",
		"port", cfg.Port,
		"window_size", cfg.WindowSize,
		"model", cfg.ModelName,
	)

	var diskLogger *agent.DiskLogger
	if logDir := envOr("LOG_DIR", ""); logDir != "" {
		var err error
		diskLogger, err = agent.NewDiskLogger(logDir)
		if err != nil {
			slog.Warn("disk logger init failed", "err", err)
		} else {
			slog.Info("disk logging enabled", "log_dir", logDir)
		}
	}

	graph := buildDefaultGraph(backends, cfg)
	s := &server{graph: graph, cfg: cfg, backends: backends, levelVar: levelVar, diskLogger: diskLogger}

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
