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

// messageParser extracts input messages from a raw request body.
type messageParser func(body []byte) []logger.Message

// outputParser extracts the output log from a raw response body and streaming flag.
type outputParser func(body []byte, streaming bool) logger.OutputLog

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

func (s *Server) HandleChatCompletions(
	w http.ResponseWriter, r *http.Request, body []byte, diskLogger *logger.DiskLogger, compactLogger *logger.CompactLogger,
) {
	tokenCount := s.countOpenAITokens(resolveBackend(s.Backends, extractModel(body)), body)
	s.forwardWithLogging(w, r, body, diskLogger, compactLogger, "openai", logger.ParseMessages, logger.ParseOpenAIOutput, tokenCount)
}

func (s *Server) HandleMessages(w http.ResponseWriter, r *http.Request, body []byte, diskLogger *logger.DiskLogger, compactLogger *logger.CompactLogger) {
	tokenCount := s.countAnthropicTokens(resolveBackend(s.Backends, extractModel(body)), body)
	s.forwardWithLogging(w, r, body, diskLogger, compactLogger, "anthropic", logger.ParseAnthropicMessages, logger.ParseAnthropicOutput, tokenCount)
}

func (s *Server) forwardWithLogging(
	w http.ResponseWriter,
	r *http.Request,
	body []byte,
	diskLogger *logger.DiskLogger,
	compactLogger *logger.CompactLogger,
	format string,
	msgParser messageParser,
	outParser outputParser,
	tokenCount int,
) {
	model := extractModel(body)
	baseURL := resolveBackend(s.Backends, model)

	var req struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &req) //nolint:errcheck // malformed body → Stream=false is safe

	// Apply routing rules based on token threshold.
	targetModel := ""
	for _, rule := range s.RoutingRules {
		if model == rule.SourceModel && tokenCount >= rule.Threshold {
			slog.Info("route override",
				"from", model,
				"to", rule.TargetModel,
				"tokens", tokenCount,
				"threshold", rule.Threshold)
			targetModel = rule.TargetModel
			break
		}
	}
	if targetModel != "" {
		baseURL = resolveBackend(s.Backends, targetModel)
	}

	if diskLogger == nil && compactLogger == nil {
		forwardRequest(r.Context(), w, r, baseURL, body)
		return
	}

	// Compact logging: extract tools, start session, store messages.
	var sessionID string
	var toolsHash string
	if compactLogger != nil {
		toolsBlob := logger.ExtractTools(body)
		if len(toolsBlob) > 0 && string(toolsBlob) != "null" {
			toolsHash = compactLogger.StoreTools(toolsBlob)
		}
		sessionID, _ = compactLogger.StartSession(toolsHash, format, tokenCount)

		// Store each message individually (globally deduplicated).
		msgs := msgParser(body)
		for _, msg := range msgs {
			msgBody, _ := json.Marshal(msg)
			if mh := compactLogger.StoreMessage(string(msgBody)); mh != "" {
				_ = compactLogger.AddMessageToSession(sessionID, mh)
			}
		}
	}

	var reqLog *logger.RequestLog
	if diskLogger != nil {
		reqLog = diskLogger.Start(format, r.URL.Path, r.Header, body)
		defer diskLogger.Save(reqLog)
	}

	rc := &responseCapture{ResponseWriter: w}
	forwardRequest(r.Context(), rc, r, baseURL, body)
	if reqLog != nil {
		reqLog.SetCall(msgParser(body), outParser(rc.buf.Bytes(), req.Stream))
	}

	// Store the output message (globally deduplicated).
	if compactLogger != nil && sessionID != "" {
		output := outParser(rc.buf.Bytes(), req.Stream)
		outputBody, _ := json.Marshal(map[string]any{
			"role":       "assistant",
			"content":    output.Content,
			"reasoning":  output.Reasoning,
			"tool_calls": output.ToolCalls,
		})
		if mh := compactLogger.StoreMessage(string(outputBody)); mh != "" {
			_ = compactLogger.AddMessageToSession(sessionID, mh)
		}
	}
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

func (s *Server) countOpenAITokens(baseURL string, body []byte) int {
	url := baseURL + "/tokenize"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		slog.Warn("tokenize build failed", "err", err)
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("tokenize request failed", "err", err)
		return 0
	}
	defer resp.Body.Close()

	var raw struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		slog.Warn("tokenize decode failed", "err", err)
		return 0
	}
	return raw.Count
}

func (s *Server) countAnthropicTokens(baseURL string, body []byte) int {
	url := baseURL + "/v1/messages/count_tokens"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		slog.Warn("count_tokens build failed", "err", err)
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("count_tokens request failed", "err", err)
		return 0
	}
	defer resp.Body.Close()

	var raw struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		slog.Warn("count_tokens decode failed", "err", err)
		return 0
	}
	return raw.InputTokens
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
