package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

func (s *server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req minimalRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	window := req.Messages
	if len(window) > s.cfg.WindowSize {
		window = window[len(window)-s.cfg.WindowSize:]
	}

	enableThinking, err := classify(r.Context(), window, s.cfg, r.Header.Get("Authorization"))
	if err != nil {
		slog.Warn("classification failed, defaulting to thinking=true", "err", err)
		enableThinking = true
		classifyErrorsTotal.Inc()
	}

	classification := "direct"
	if enableThinking {
		classification = "reasoning"
	}
	slog.Debug("routed request", "classification", classification, "messages", len(req.Messages))
	defer func() {
		requestsTotal.WithLabelValues(classification).Inc()
		requestDurationSeconds.WithLabelValues(classification).Observe(time.Since(start).Seconds())
	}()

	modifiedBody, err := injectThinkingMode(bodyBytes, enableThinking)
	if err != nil {
		http.Error(w, "failed to modify request", http.StatusInternalServerError)
		return
	}

	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		s.cfg.VLLMBaseURL+"/v1/chat/completions", bytes.NewReader(modifiedBody))
	if err != nil {
		http.Error(w, "failed to build upstream request", http.StatusInternalServerError)
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	if auth := r.Header.Get("Authorization"); auth != "" {
		upstream.Header.Set("Authorization", auth)
	}

	resp, err := http.DefaultClient.Do(upstream)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)

	if req.Stream {
		proxyStream(w, resp.Body)
	} else {
		io.Copy(w, resp.Body)
	}
}

func (s *server) handleGenericProxy(w http.ResponseWriter, r *http.Request) {
	upstream, err := http.NewRequestWithContext(r.Context(), r.Method,
		s.cfg.VLLMBaseURL+r.RequestURI, r.Body)
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

	copyResponseHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func proxyStream(w http.ResponseWriter, body io.Reader) {
	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
}

func copyResponseHeaders(w http.ResponseWriter, resp *http.Response) {
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
}
