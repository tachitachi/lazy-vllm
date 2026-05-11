package logger

import (
	_ "embed"
	"encoding/json"
	"net/http"
)

//go:embed ui/logs.html
var logsHTML []byte

func (d *DiskLogger) HandleLogsUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(logsHTML)
}

func (d *DiskLogger) HandleLogsList(w http.ResponseWriter, r *http.Request) {
	summaries, err := d.ListLogs(7)
	if err != nil {
		http.Error(w, "failed to list logs", http.StatusInternalServerError)
		return
	}
	if summaries == nil {
		summaries = []LogSummary{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summaries)
}

func (d *DiskLogger) HandleLogDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	log, err := d.GetLog(id, 7)
	if err != nil {
		http.Error(w, "log not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(log)
}
