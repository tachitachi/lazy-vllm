package logger

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Message mirrors the shape expected by the log UI (role + content + optional fields).
// Content is kept as raw JSON to handle both string and content-block array forms.
type Message struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning,omitempty"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
}

type ToolCall struct {
	ID       string   `json:"id,omitempty"`
	Type     string   `json:"type,omitempty"`
	Function ToolFunc `json:"function"`
}

type ToolFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type OutputLog struct {
	Reasoning string     `json:"reasoning,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type CallLog struct {
	NodeID    string    `json:"node_id"`
	NodeKind  string    `json:"node_kind"`
	Iteration int       `json:"iteration"`
	Timestamp time.Time `json:"timestamp"`
	Input     []Message `json:"input"`
	Output    OutputLog `json:"output"`
}

type RequestLog struct {
	ID             string            `json:"id"`
	StartedAt      time.Time         `json:"started_at"`
	FinishedAt     time.Time         `json:"finished_at"`
	DurationMS     int64             `json:"duration_ms"`
	Format         string            `json:"format"`
	RequestPath    string            `json:"request_path"`
	RequestHeaders map[string]string `json:"request_headers"`
	RequestBody    json.RawMessage   `json:"request_body"`
	Calls          []CallLog         `json:"calls"`
	mu             sync.Mutex
}

func (r *RequestLog) SetCall(input []Message, output OutputLog) {
	r.mu.Lock()
	r.Calls = []CallLog{{
		NodeID:    "proxy",
		NodeKind:  "proxy",
		Timestamp: time.Now(),
		Input:     input,
		Output:    output,
	}}
	r.mu.Unlock()
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
	db *sql.DB
}

// migrations holds schema changes grouped by version. Index i contains the
// statements to apply when upgrading from version i to i+1. Add new entries
// at the end; never edit existing ones.
var migrations = [][]string{
	// v0 → v1: initial schema
	{
		`CREATE TABLE IF NOT EXISTS requests (
			id              TEXT    PRIMARY KEY,
			started_at      INTEGER NOT NULL,
			finished_at     INTEGER NOT NULL DEFAULT 0,
			duration_ms     INTEGER NOT NULL DEFAULT 0,
			format          TEXT    NOT NULL DEFAULT '',
			request_path    TEXT    NOT NULL DEFAULT '',
			request_headers TEXT    NOT NULL DEFAULT '{}',
			request_body    TEXT    NOT NULL DEFAULT 'null',
			calls           TEXT    NOT NULL DEFAULT '[]'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_started_at ON requests (started_at DESC)`,
	},
}

func migrate(db *sql.DB) error {
	var version int
	db.QueryRow(`PRAGMA user_version`).Scan(&version)
	for i := version; i < len(migrations); i++ {
		for _, stmt := range migrations[i] {
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("migration %d: %w", i+1, err)
			}
		}
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			return fmt.Errorf("migration %d: bump version: %w", i+1, err)
		}
	}
	return nil
}

func New(logDir string) (*DiskLogger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("logger: create log dir: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(logDir, "logs.db"))
	if err != nil {
		return nil, fmt.Errorf("logger: open sqlite: %w", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("logger: set WAL mode: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("logger: %w", err)
	}
	return &DiskLogger{db: db}, nil
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

	headers, _ := json.Marshal(reqLog.RequestHeaders)
	calls, _ := json.Marshal(reqLog.Calls)

	d.db.Exec(
		`INSERT OR REPLACE INTO requests
			(id, started_at, finished_at, duration_ms, format, request_path, request_headers, request_body, calls)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reqLog.ID,
		reqLog.StartedAt.UnixMilli(),
		reqLog.FinishedAt.UnixMilli(),
		reqLog.DurationMS,
		reqLog.Format,
		reqLog.RequestPath,
		string(headers),
		string(reqLog.RequestBody),
		string(calls),
	)
}

func (d *DiskLogger) ListLogs(days int) ([]LogSummary, error) {
	cutoff := time.Now().AddDate(0, 0, -days).UnixMilli()
	rows, err := d.db.Query(
		`SELECT id, started_at, finished_at, duration_ms, format, request_path
		 FROM requests WHERE started_at >= ? ORDER BY started_at DESC`,
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []LogSummary
	for rows.Next() {
		var s LogSummary
		var startedMS, finishedMS int64
		if err := rows.Scan(&s.ID, &startedMS, &finishedMS, &s.DurationMS, &s.Format, &s.RequestPath); err != nil {
			continue
		}
		s.StartedAt = time.UnixMilli(startedMS)
		s.FinishedAt = time.UnixMilli(finishedMS)
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

func (d *DiskLogger) GetLog(id string, _ int) (*RequestLog, error) {
	row := d.db.QueryRow(
		`SELECT id, started_at, finished_at, duration_ms, format, request_path, request_headers, request_body, calls
		 FROM requests WHERE id = ?`,
		id,
	)
	var log RequestLog
	var startedMS, finishedMS int64
	var headers, body, calls string
	if err := row.Scan(&log.ID, &startedMS, &finishedMS, &log.DurationMS, &log.Format, &log.RequestPath, &headers, &body, &calls); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("log %q not found", id)
		}
		return nil, err
	}
	log.StartedAt = time.UnixMilli(startedMS)
	log.FinishedAt = time.UnixMilli(finishedMS)
	json.Unmarshal([]byte(headers), &log.RequestHeaders)
	log.RequestBody = json.RawMessage(body)
	json.Unmarshal([]byte(calls), &log.Calls)
	return &log, nil
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
