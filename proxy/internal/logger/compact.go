package logger

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"
)

// CompactLogger stores conversation data in SQLite with O(n) storage.
// Messages are globally deduplicated by SHA256(body).
// Sessions reference messages via a join table and own a tools_hash.

const schemaVersion = 4

// CompactSession summarizes a stored conversation session.
type CompactSession struct {
	ID             string    `json:"id"`
	Format         string    `json:"format"`
	Model          string    `json:"model"`
	TokenCount     int       `json:"token_count"`
	CreatedAt      time.Time `json:"created_at"`
	MessageCt      int       `json:"message_count"`
	LastDurationMS *int64    `json:"last_duration_ms,omitempty"`
	ToolsHash      *string   `json:"tools_hash,omitempty"`
	Summary        *string   `json:"summary,omitempty"`
}

// CompactSessionMessage holds a message reference within a session.
type CompactSessionMessage struct {
	Sequence    int       `json:"sequence"`
	MessageHash string    `json:"message_hash"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
	DurationMS  int64     `json:"duration_ms"`
}

// RawRequest holds the raw inbound request for a session.
type RawRequest struct {
	SessionID string            `json:"session_id"`
	Path      string            `json:"path"`
	Headers   map[string]string `json:"headers"`
	Body      []byte            `json:"body"`
	CreatedAt time.Time         `json:"created_at"`
}

var capturedRequestHeaders = []string{"Authorization", "x-api-key", "anthropic-version", "anthropic-beta"}
var redactedRequestHeaders = map[string]bool{"Authorization": true, "x-api-key": true}

// CaptureHeaders extracts a filtered subset of HTTP headers for debug logging.
// Auth header values are replaced with "[redacted]".
func CaptureHeaders(h http.Header) map[string]string {
	result := make(map[string]string)
	for _, name := range capturedRequestHeaders {
		if v := h.Get(name); v != "" {
			if redactedRequestHeaders[name] {
				result[name] = "[redacted]"
			} else {
				result[name] = v
			}
		}
	}
	return result
}

type CompactLogger struct {
	db *sql.DB
}

// toolsHash computes a deterministic SHA256 hash of a canonical tools JSON string.
func toolsHash(body []byte) []byte {
	h := sha256.Sum256(body)
	return h[:]
}

// bodyHash computes a deterministic SHA256 hash of a message body.
func bodyHash(body string) []byte {
	h := sha256.Sum256([]byte(body))
	return h[:]
}

// hashHex converts a raw SHA256 hash to a hex string for API/JSON use.
func hashHex(hash []byte) string {
	return hex.EncodeToString(hash)
}

// hexHash converts a hex string back to a raw hash for DB queries.
func hexHash(hexStr string) []byte {
	b, _ := hex.DecodeString(hexStr)
	return b
}

// compressData compresses data using zstd. Returns the original data if
// compression doesn't help (very small payloads).
// compressData compresses data using zstd. Returns the original data if
// compression doesn't help (very small payloads).
func compressData(data []byte) []byte {
	out := zstd.EncodeTo(nil, data)
	if len(out) >= len(data) {
		return data
	}
	return out
}

// decompressData decompresses zstd-compressed data. Falls back to the
// raw bytes if decompression fails (e.g., uncompressed legacy data).
func decompressData(data []byte) []byte {
	out, err := zstd.DecodeTo(nil, data)
	if err != nil {
		return data
	}
	return out
}

// NewCompact creates (or opens) the compact logger database.
func NewCompact(logDir string) (*CompactLogger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("compact logger: create log dir: %w", err)
	}
	dbPath := filepath.Join(logDir, "compact_logs.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("compact logger: open sqlite: %w", err)
	}
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("compact logger: set WAL mode: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("compact logger: enable foreign keys: %w", err)
	}

	// Read current schema version (0 if table or row doesn't exist yet).
	var version int
	_ = db.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version)

	if version < schemaVersion {
		if err := migrateCompact(ctx, db, version); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("compact logger: migrate: %w", err)
		}
	}

	return &CompactLogger{db: db}, nil
}

// migrateCompact applies incremental schema migrations from fromVersion to schemaVersion.
func migrateCompact(ctx context.Context, db *sql.DB, fromVersion int) error {
	if fromVersion < 3 {
		// Drop any partial/legacy state and build the v3 base schema from scratch.
		for _, tbl := range []string{"session_messages", "sessions", "messages", "tools", "schema_version", "raw_requests"} {
			db.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl) //nolint:errcheck
		}
		stmts := []string{
			`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY)`,
			`CREATE TABLE IF NOT EXISTS tools (
				hash       BLOB    PRIMARY KEY,
				body       BLOB    NOT NULL,
				created_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS messages (
				hash       BLOB    PRIMARY KEY,
				body       BLOB    NOT NULL,
				created_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS sessions (
				id          TEXT    PRIMARY KEY,
				format      TEXT    NOT NULL,
				model       TEXT    NOT NULL DEFAULT '',
				token_count INTEGER NOT NULL DEFAULT 0,
				tools_hash  BLOB,
				summary     TEXT,
				created_at  INTEGER NOT NULL,
				FOREIGN KEY(tools_hash) REFERENCES tools(hash)
			)`,
			`CREATE TABLE IF NOT EXISTS session_messages (
				session_id   TEXT    NOT NULL,
				message_hash BLOB    NOT NULL,
				sequence     INTEGER NOT NULL,
				duration_ms  INTEGER NOT NULL DEFAULT 0,
				UNIQUE(session_id, sequence),
				FOREIGN KEY(session_id) REFERENCES sessions(id),
				FOREIGN KEY(message_hash) REFERENCES messages(hash)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_created ON sessions(created_at DESC)`,
			`INSERT OR REPLACE INTO schema_version (version) VALUES (3)`,
		}
		for _, stmt := range stmts {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("v3 base schema: %w", err)
			}
		}
		fromVersion = 3
	}

	if fromVersion < 4 {
		stmts := []string{
			`CREATE TABLE IF NOT EXISTS raw_requests (
				session_id TEXT    NOT NULL,
				path       TEXT    NOT NULL,
				headers    TEXT    NOT NULL DEFAULT '{}',
				body       BLOB    NOT NULL,
				created_at INTEGER NOT NULL,
				FOREIGN KEY(session_id) REFERENCES sessions(id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_raw_requests_session ON raw_requests(session_id)`,
			`UPDATE schema_version SET version = 4`,
		}
		for _, stmt := range stmts {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("v4 raw_requests: %w", err)
			}
		}
	}

	return nil
}

// Close closes the underlying database connection.
func (c *CompactLogger) Close() error {
	return c.db.Close()
}

// StartSession creates a new session with the given tools hash, format, model, and token count.
func (c *CompactLogger) StartSession(toolsHash, format, model string, tokenCount int) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("compact logger: rand: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)

	var toolsBlob *[]byte
	if toolsHash != "" {
		b := hexHash(toolsHash)
		toolsBlob = &b
	}
	if _, err := c.db.ExecContext(context.Background(),
		`INSERT INTO sessions (id, format, model, token_count, tools_hash, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, format, model, tokenCount, toolsBlob, time.Now().UnixMilli(),
	); err != nil {
		return "", fmt.Errorf("compact logger: insert session: %w", err)
	}
	return id, nil
}

// StoreTools deduplicates a tools JSON blob, returning its hex-encoded hash.
func (c *CompactLogger) StoreTools(body []byte) string {
	th := toolsHash(body)
	compressed := compressData(body)
	if _, err := c.db.ExecContext(context.Background(),
		`INSERT OR IGNORE INTO tools (hash, body, created_at) VALUES (?, ?, ?)`,
		th, compressed, time.Now().UnixMilli(),
	); err != nil {
		return ""
	}
	return hashHex(th)
}

// StoreMessage deduplicates a message by body content, returning its hex-encoded hash.
func (c *CompactLogger) StoreMessage(body string) string {
	bh := bodyHash(body)
	compressed := compressData([]byte(body))
	if _, err := c.db.ExecContext(context.Background(),
		`INSERT OR IGNORE INTO messages (hash, body, created_at) VALUES (?, ?, ?)`,
		bh, compressed, time.Now().UnixMilli(),
	); err != nil {
		return ""
	}
	return hashHex(bh)
}

// AddMessageToSession adds a message hash to a session's message chain.
func (c *CompactLogger) AddMessageToSession(sessionID, messageHash string, durationMS int64) error {
	bh := hexHash(messageHash)
	var seq int
	err := c.db.QueryRowContext(context.Background(),
		"SELECT COALESCE(MAX(sequence), 0) FROM session_messages WHERE session_id = ?",
		sessionID,
	).Scan(&seq)
	if err != nil && errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = c.db.ExecContext(context.Background(),
		`INSERT INTO session_messages (session_id, message_hash, sequence, duration_ms) VALUES (?, ?, ?, ?)`,
		sessionID, bh, seq+1, durationMS,
	)
	return err
}

// SetSessionSummary stores the extracted observation for a session.
func (c *CompactLogger) SetSessionSummary(sessionID, summary string) error {
	_, err := c.db.ExecContext(context.Background(),
		`UPDATE sessions SET summary = ? WHERE id = ?`,
		summary, sessionID,
	)
	return err
}

// GetSession reconstructs a conversation, returning messages in order with session metadata.
func (c *CompactLogger) GetSession(id string) (*CompactSession, []CompactSessionMessage, error) {
	// Get session metadata
	var session CompactSession
	var toolsHashBlob *[]byte
	var created int64
	err := c.db.QueryRowContext(context.Background(),
		`SELECT id, format, model, token_count, tools_hash, summary, created_at FROM sessions WHERE id = ?`,
		id,
	).Scan(&session.ID, &session.Format, &session.Model, &session.TokenCount, &toolsHashBlob, &session.Summary, &created)
	if err != nil {
		return nil, nil, err
	}
	session.CreatedAt = time.UnixMilli(created)
	if toolsHashBlob != nil {
		h := hashHex(*toolsHashBlob)
		session.ToolsHash = &h
	}

	// Get messages in sequence order
	rows, err := c.db.QueryContext(context.Background(),
		`SELECT sm.sequence, m.hash, m.body, m.created_at, sm.duration_ms
		 FROM session_messages sm
		 JOIN messages m ON m.hash = sm.message_hash
		 WHERE sm.session_id = ?
		 ORDER BY sm.sequence ASC`,
		id,
	)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()

	var msgs []CompactSessionMessage
	for rows.Next() {
		var msg CompactSessionMessage
		var hash []byte
		var body []byte
		var created int64
		if err := rows.Scan(&msg.Sequence, &hash, &body, &created, &msg.DurationMS); err != nil {
			continue
		}
		msg.Body = string(decompressData(body))
		msg.MessageHash = hashHex(hash)
		msg.CreatedAt = time.UnixMilli(created)
		msgs = append(msgs, msg)
	}
	return &session, msgs, nil
}

// ListSessions returns recent sessions (last n days).
func (c *CompactLogger) ListSessions(days int) ([]CompactSession, error) {
	latestDuration := `(SELECT sm2.duration_ms FROM session_messages sm2 WHERE sm2.session_id = s.id ORDER BY sm2.sequence DESC LIMIT 1)`
	query := `SELECT s.id, s.format, s.model, s.token_count, s.tools_hash, s.summary, s.created_at, COUNT(sm.message_hash),` + latestDuration + `
		 FROM sessions s
		 LEFT JOIN session_messages sm ON sm.session_id = s.id
		 WHERE s.created_at >= ?
		 GROUP BY s.id
		 ORDER BY s.created_at DESC`

	cutoff := time.Now().AddDate(0, 0, -days).UnixMilli()
	rows, err := c.db.QueryContext(context.Background(), query, cutoff)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var sessions []CompactSession
	for rows.Next() {
		var s CompactSession
		var created int64
		var toolsHashBlob *[]byte
		var lastDuration *int64
		if err := rows.Scan(&s.ID, &s.Format, &s.Model, &s.TokenCount, &toolsHashBlob, &s.Summary, &created, &s.MessageCt, &lastDuration); err != nil {
			continue
		}
		s.CreatedAt = time.UnixMilli(created)
		s.LastDurationMS = lastDuration
		if toolsHashBlob != nil {
			h := hashHex(*toolsHashBlob)
			s.ToolsHash = &h
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// GetTools retrieves a tools JSON blob by its hex-encoded hash.
func (c *CompactLogger) GetTools(hash string) (string, error) {
	bh := hexHash(hash)
	var body []byte
	err := c.db.QueryRowContext(context.Background(), "SELECT body FROM tools WHERE hash = ?", bh).Scan(&body)
	if err != nil {
		return "", err
	}
	return string(decompressData(body)), nil
}

// StoreRawRequest saves the raw inbound request for a session.
func (c *CompactLogger) StoreRawRequest(sessionID, path string, headers map[string]string, body []byte) error {
	headersJSON, _ := json.Marshal(headers)
	compressed := compressData(body)
	_, err := c.db.ExecContext(context.Background(),
		`INSERT INTO raw_requests (session_id, path, headers, body, created_at) VALUES (?, ?, ?, ?, ?)`,
		sessionID, path, string(headersJSON), compressed, time.Now().UnixMilli(),
	)
	return err
}

// GetRawRequest retrieves the raw request for a session.
func (c *CompactLogger) GetRawRequest(sessionID string) (*RawRequest, error) {
	var rr RawRequest
	var headersStr string
	var body []byte
	var created int64
	err := c.db.QueryRowContext(context.Background(),
		`SELECT session_id, path, headers, body, created_at FROM raw_requests WHERE session_id = ?`,
		sessionID,
	).Scan(&rr.SessionID, &rr.Path, &headersStr, &body, &created)
	if err != nil {
		return nil, err
	}
	rr.Body = decompressData(body)
	rr.CreatedAt = time.UnixMilli(created)
	_ = json.Unmarshal([]byte(headersStr), &rr.Headers)
	return &rr, nil
}
