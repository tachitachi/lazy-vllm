package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"lazy-vllm-proxy/internal/logger"
)

func flushCopy(w http.ResponseWriter, r io.Reader) {
	buf := make([]byte, 4096)
	flusher, canFlush := w.(http.Flusher)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
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
	flushCopy(w, resp.Body)
}

func (s *Server) HandleChatCompletions(w http.ResponseWriter, r *http.Request, body []byte, diskLogger *logger.DiskLogger) {
	model := extractModel(body)
	baseURL := resolveBackend(s.Backends, model)

	var req struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &req) //nolint:errcheck // malformed body → Stream=false is safe

	if diskLogger == nil {
		forwardRequest(r.Context(), w, r, baseURL, body)
		return
	}

	reqLog := diskLogger.Start("openai", r.URL.Path, r.Header, body)
	defer diskLogger.Save(reqLog)

	rc := &responseCapture{ResponseWriter: w}
	forwardRequest(r.Context(), rc, r, baseURL, body)
	reqLog.SetCall(
		logger.ParseMessages(body),
		logger.ParseOpenAIOutput(rc.buf.Bytes(), req.Stream),
	)
}

func (s *Server) HandleMessages(w http.ResponseWriter, r *http.Request, body []byte, diskLogger *logger.DiskLogger) {
	model := extractModel(body)
	baseURL := resolveBackend(s.Backends, model)

	var req struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &req) //nolint:errcheck // malformed body → Stream=false is safe

	if diskLogger == nil {
		forwardRequest(r.Context(), w, r, baseURL, body)
		return
	}

	reqLog := diskLogger.Start("anthropic", r.URL.Path, r.Header, body)
	defer diskLogger.Save(reqLog)

	rc := &responseCapture{ResponseWriter: w}
	forwardRequest(r.Context(), rc, r, baseURL, body)
	reqLog.SetCall(
		logger.ParseAnthropicMessages(body),
		logger.ParseAnthropicOutput(rc.buf.Bytes(), req.Stream),
	)
}

func (s *Server) HandleGenericProxy(w http.ResponseWriter, r *http.Request, body []byte) {
	baseURL := resolveBackend(s.Backends, extractModel(body))
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
	flushCopy(w, resp.Body)
}

func (s *Server) HandleLogLevel(w http.ResponseWriter, r *http.Request) {
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
	case "WARN", "WARNING":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		http.Error(w, "invalid log level", http.StatusBadRequest)
		return
	}
	s.LevelVar.Set(level)
	slog.Info("log level changed", "new_level", level.String())
	w.WriteHeader(http.StatusOK)
}
