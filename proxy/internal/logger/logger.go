package logger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type RequestLog struct {
	ID             string            `json:"id"`
	StartedAt      time.Time         `json:"started_at"`
	FinishedAt     time.Time         `json:"finished_at"`
	DurationMS     int64             `json:"duration_ms"`
	Format         string            `json:"format"`
	RequestPath    string            `json:"request_path"`
	RequestHeaders map[string]string `json:"request_headers"`
	RequestBody    json.RawMessage   `json:"request_body"`
	mu             sync.Mutex
}

type LogSummary struct {
	ID          string    `json:"id"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	DurationMS  int64     `json:"duration_ms"`
	Format      string    `json:"format"`
	RequestPath string    `json:"request_path"`
}

type DiskLogger struct {
	logDir string
}

func New(logDir string) (*DiskLogger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("logger: create log dir: %w", err)
	}
	return &DiskLogger{logDir: logDir}, nil
}

var capturedHeaders = []string{
	"Authorization", "x-api-key", "Content-Type",
	"anthropic-version", "anthropic-beta",
}

func (d *DiskLogger) Start(format, requestPath string, headers http.Header, body []byte) *RequestLog {
	captured := make(map[string]string)
	for _, h := range capturedHeaders {
		if v := headers.Get(h); v != "" {
			captured[h] = v
		}
	}
	return &RequestLog{
		ID:             newID(),
		StartedAt:      time.Now(),
		Format:         format,
		RequestPath:    requestPath,
		RequestHeaders: captured,
		RequestBody:    json.RawMessage(body),
	}
}

func (d *DiskLogger) Save(reqLog *RequestLog) {
	reqLog.mu.Lock()
	reqLog.FinishedAt = time.Now()
	reqLog.DurationMS = reqLog.FinishedAt.Sub(reqLog.StartedAt).Milliseconds()
	reqLog.mu.Unlock()

	dateDir := filepath.Join(d.logDir, reqLog.StartedAt.Format("2006-01-02"))
	if err := os.MkdirAll(dateDir, 0755); err != nil {
		return
	}
	data, err := json.MarshalIndent(reqLog, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(filepath.Join(dateDir, reqLog.ID+".json"), data, 0644)
}

func (d *DiskLogger) ListLogs(days int) ([]LogSummary, error) {
	var summaries []LogSummary
	now := time.Now()
	for i := 0; i < days; i++ {
		dateDir := filepath.Join(d.logDir, now.AddDate(0, 0, -i).Format("2006-01-02"))
		entries, err := os.ReadDir(dateDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dateDir, entry.Name()))
			if err != nil {
				continue
			}
			var log RequestLog
			if err := json.Unmarshal(data, &log); err != nil {
				continue
			}
			summaries = append(summaries, LogSummary{
				ID:          log.ID,
				StartedAt:   log.StartedAt,
				FinishedAt:  log.FinishedAt,
				DurationMS:  log.DurationMS,
				Format:      log.Format,
				RequestPath: log.RequestPath,
			})
		}
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].StartedAt.After(summaries[j].StartedAt)
	})
	return summaries, nil
}

func (d *DiskLogger) GetLog(id string, days int) (*RequestLog, error) {
	now := time.Now()
	for i := 0; i < days; i++ {
		path := filepath.Join(d.logDir, now.AddDate(0, 0, -i).Format("2006-01-02"), id+".json")
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var log RequestLog
		if err := json.Unmarshal(data, &log); err != nil {
			return nil, err
		}
		return &log, nil
	}
	return nil, fmt.Errorf("log %q not found", id)
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

type ctxKey int

const reqLogKey ctxKey = 0

func WithReqLog(ctx context.Context, log *RequestLog) context.Context {
	return context.WithValue(ctx, reqLogKey, log)
}

func ReqLogFromCtx(ctx context.Context) *RequestLog {
	log, _ := ctx.Value(reqLogKey).(*RequestLog)
	return log
}
