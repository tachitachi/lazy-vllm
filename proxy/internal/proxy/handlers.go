package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

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
		baseURL+r.URL.RequestURI(), io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		slog.Error("build upstream request failed", "err", err, "path", r.RequestURI)
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
		slog.Error("upstream request failed", "err", err, "url", baseURL+r.RequestURI)
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
	w http.ResponseWriter, r *http.Request, body []byte, compactLogger *logger.CompactLogger,
) {
	provider := ProviderFromCtx(r.Context())
	if provider != "" && provider != "local" {
		p, _ := lookupProvider(s.Providers, provider)
		model := modelFromBody(body)
		tokenCount := 0
		if p.CountTokens {
			tokenCount = s.countOpenAITokens(p.URL, body)
		}
		s.forwardWithLogging(w, r, body, compactLogger, "openai", logger.ParseMessages, logger.ParseOpenAIOutput, tokenCount, model, false, p.InjectObsInstructions, false)
		return
	}
	model, cleanedBody := extractModel(body)
	backend := findBackend(s.Backends, stripFlash(model))
	tokenCount := 0
	if backend.CountTokens {
		tokenCount = s.countOpenAITokens(backend.URL, cleanedBody)
	}
	s.forwardWithLogging(w, r, cleanedBody, compactLogger, "openai", logger.ParseMessages, logger.ParseOpenAIOutput, tokenCount, model, isFlashModel(model), backend.InjectObsInstructions, backend.InjectThinkingBudget)
}

func (s *Server) HandleMessages(w http.ResponseWriter, r *http.Request, body []byte, compactLogger *logger.CompactLogger) {
	provider := ProviderFromCtx(r.Context())
	if provider != "" && provider != "local" {
		p, _ := lookupProvider(s.Providers, provider)
		model := modelFromBody(body)
		tokenCount := 0
		if p.CountTokens {
			tokenCount = s.countAnthropicTokens(p.URL, body)
		}
		s.forwardWithLogging(w, r, body, compactLogger, "anthropic", logger.ParseAnthropicMessages, logger.ParseAnthropicOutput, tokenCount, model, false, p.InjectObsInstructions, false)
		return
	}
	model, cleanedBody := extractModel(body)
	backend := findBackend(s.Backends, stripFlash(model))
	tokenCount := 0
	if backend.CountTokens {
		tokenCount = s.countAnthropicTokens(backend.URL, cleanedBody)
	}
	s.forwardWithLogging(w, r, cleanedBody, compactLogger, "anthropic", logger.ParseAnthropicMessages, logger.ParseAnthropicOutput, tokenCount, model, isFlashModel(model), backend.InjectObsInstructions, backend.InjectThinkingBudget)
}

func (s *Server) forwardWithLogging(
	w http.ResponseWriter,
	r *http.Request,
	body []byte,
	compactLogger *logger.CompactLogger,
	format string,
	msgParser messageParser,
	outParser outputParser,
	tokenCount int,
	model string,
	isFlash bool,
	injectObs bool,
	injectThinkingBudget bool,
) {
	provider := ProviderFromCtx(r.Context())
	user     := UserFromCtx(r.Context())
	project  := ProjectFromCtx(r.Context())

	var baseURL string
	var bodyToForward []byte
	if provider != "" && provider != "local" {
		p, _ := lookupProvider(s.Providers, provider)
		baseURL = p.URL
		// Non-local: no FLASH injection; obs instruction applies when enabled
		if injectObs && compactLogger != nil && !isProbeRequest(body) {
			bodyToForward = injectObsInstruction(body, format)
		} else {
			bodyToForward = body
		}
	} else {
		// Local: apply FLASH, thinking budget, and routing rules
		budget, disableThinking := computeThinkingBudget(body, s.ThinkingBudgetFraction)
		thinkDisabledByClient := requestDisableThinking(body)
		effectiveFlash := isFlash || disableThinking || thinkDisabledByClient

		if effectiveFlash {
			bodyToForward = injectDisableThinking(body)
		} else if injectObs && compactLogger != nil && !isProbeRequest(body) {
			bodyToForward = injectObsInstruction(body, format)
		} else {
			bodyToForward = body
		}

		if !effectiveFlash && injectThinkingBudget {
			if injected, err := injectThinkingTokenBudget(bodyToForward, budget); err == nil {
				bodyToForward = injected
			} else {
				slog.Warn("failed to inject thinking token budget", "err", err)
			}
		}
		baseURL = resolveBackend(s.Backends, stripFlash(model))
		realModel := stripFlash(model)
		for _, rule := range s.RoutingRules {
			if realModel == rule.SourceModel && tokenCount >= rule.Threshold {
				slog.Info("route override",
					"from", model,
					"to", rule.TargetModel,
					"tokens", tokenCount,
					"threshold", rule.Threshold)
				baseURL = resolveBackend(s.Backends, rule.TargetModel)
				break
			}
		}
	}

	var req struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &req) //nolint:errcheck // malformed body → Stream=false is safe

	if compactLogger == nil {
		forwardRequest(r.Context(), w, r, baseURL, bodyToForward)
		return
	}

	// Compact logging: extract tools, start session, store messages.
	var sessionID string
	var toolsHash string
	if compactLogger != nil {
		toolsBlob := logger.ExtractTools(bodyToForward)
		if len(toolsBlob) > 0 && string(toolsBlob) != "null" {
			toolsHash = compactLogger.StoreTools(toolsBlob)
		}
		var err error
		sessionID, err = compactLogger.StartSession(toolsHash, format, model, user, project, provider, tokenCount)
		if err != nil {
			slog.Warn("compact logger: start session failed", "err", err)
		}

		if sessionID != "" {
			if err := compactLogger.StoreRawRequest(sessionID, r.RequestURI, logger.CaptureHeaders(r.Header), body); err != nil {
				slog.Warn("compact logger: store raw request failed", "err", err)
			}
		}

		// Store each message individually (globally deduplicated).
		msgs := msgParser(bodyToForward)
		for _, msg := range msgs {
			msgBody, _ := json.Marshal(msg)
			if mh := compactLogger.StoreMessage(string(msgBody)); mh != "" {
				_ = compactLogger.AddMessageToSession(sessionID, mh, 0)
			}
		}
	}

	mc := newObsCapture(w, req.Stream, format)
	rc := &responseCapture{ResponseWriter: mc}
	forwardStart := time.Now()
	forwardRequest(r.Context(), rc, r, baseURL, bodyToForward)
	durationMS := time.Since(forwardStart).Milliseconds()
	obsContent := mc.Finalize()

	// Store the output message (globally deduplicated).
	if sessionID != "" {
		output := outParser(rc.buf.Bytes(), req.Stream)
		outputBody, _ := json.Marshal(map[string]any{
			"role":       "assistant",
			"content":    output.Content,
			"reasoning":  output.Reasoning,
			"tool_calls": output.ToolCalls,
		})
		if mh := compactLogger.StoreMessage(string(outputBody)); mh != "" {
			_ = compactLogger.AddMessageToSession(sessionID, mh, durationMS)
		}
	}

	if obsContent != "" && sessionID != "" {
		if err := compactLogger.SetSessionSummary(sessionID, obsContent); err != nil {
			slog.Warn("compact logger: set session summary failed", "err", err)
		}
		if s.MemoryIngestURL != "" {
			go ingestToMemory(s.MemoryIngestURL, sessionID, obsContent, model, format, user, project, provider, tokenCount)
		}
	}
}

func (s *Server) HandleGenericProxy(w http.ResponseWriter, r *http.Request, body []byte) {
	provider := ProviderFromCtx(r.Context())
	var baseURL string
	var bodyToForward []byte
	if provider != "" && provider != "local" {
		p, _ := lookupProvider(s.Providers, provider)
		baseURL = p.URL
		bodyToForward = body
	} else {
		model, cleanedBody := extractModel(body)
		baseURL = resolveBackend(s.Backends, stripFlash(model))
		bodyToForward = cleanedBody
		if isFlashModel(model) || requestDisableThinking(body) {
			bodyToForward = injectDisableThinking(body)
		}
	}
	upstream, err := http.NewRequestWithContext(r.Context(), r.Method,
		baseURL+r.URL.RequestURI(), io.NopCloser(bytes.NewReader(bodyToForward)))
	if err != nil {
		slog.Error("generic proxy: build upstream request failed", "err", err, "path", r.URL.RequestURI())
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
		slog.Error("generic proxy: upstream request failed", "err", err, "url", baseURL+r.URL.RequestURI())
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
