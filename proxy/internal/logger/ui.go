package logger

import (
	_ "embed"
	"encoding/json"
	"log/slog"
	"net/http"
)

//go:embed ui/logs.html
var logsHTML []byte

func (c *CompactLogger) HandleLogsUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(logsHTML)
}

// HandleSessionsList returns recent compact logger sessions.
func (c *CompactLogger) HandleSessionsList(w http.ResponseWriter, r *http.Request) {
	sessions, err := c.ListSessions(7)
	if err != nil {
		slog.Warn("list sessions failed", "err", err)
		http.Error(w, "failed to list sessions", http.StatusInternalServerError)
		return
	}
	if sessions == nil {
		sessions = []CompactSession{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sessions)
}

// HandleSessionDetail returns the session metadata and message chain.
func (c *CompactLogger) HandleSessionDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	session, msgs, err := c.GetSession(id)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if msgs == nil {
		msgs = []CompactSessionMessage{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"session": session, "messages": msgs})
}

// HandleToolsDetail returns a tools JSON blob by hash.
func (c *CompactLogger) HandleToolsDetail(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if hash == "" {
		http.Error(w, "missing hash", http.StatusBadRequest)
		return
	}
	body, err := c.GetTools(hash)
	if err != nil {
		http.Error(w, "tools not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// HandleRawRequest returns the raw inbound request stored for a session.
func (c *CompactLogger) HandleRawRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	rr, err := c.GetRawRequest(id)
	if err != nil {
		http.Error(w, "raw request not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"session_id": rr.SessionID,
		"path":       rr.Path,
		"headers":    rr.Headers,
		"body":       string(rr.Body),
		"created_at": rr.CreatedAt,
	})
}
