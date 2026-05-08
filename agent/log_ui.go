package main

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"agent-graph/internal/agent"
)

//go:embed ui/logs.html
var logsHTML []byte

func (s *server) handleLogsUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(logsHTML)
}

func (s *server) handleLogsList(w http.ResponseWriter, r *http.Request) {
	summaries, err := s.diskLogger.ListLogs(7)
	if err != nil {
		http.Error(w, "failed to list logs", http.StatusInternalServerError)
		return
	}
	if summaries == nil {
		summaries = []agent.LogSummary{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summaries)
}

func (s *server) handleLogDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	log, err := s.diskLogger.GetLog(id, 7)
	if err != nil {
		http.Error(w, "log not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(log)
}
