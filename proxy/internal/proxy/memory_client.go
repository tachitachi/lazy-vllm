package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

func ingestToMemory(baseURL, sessionID, summary, model, format, user, project string, tokenCount int) {
	p := map[string]any{
		"session_id":    sessionID,
		"summary":       summary,
		"model":         model,
		"format":        format,
		"token_count":   tokenCount,
		"created_at_ms": time.Now().UnixMilli(),
	}
	if user != "" {
		p["user"] = user
	}
	if project != "" {
		p["project"] = project
	}
	payload, _ := json.Marshal(p)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/ingest", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("memory ingest failed", "err", err)
		return
	}
	defer resp.Body.Close()
}
