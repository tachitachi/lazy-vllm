package logger

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// CompactLogger stores conversation data in SQLite with O(n) storage.
// Messages are globally deduplicated by SHA256(body).
// Sessions reference messages via a join table and own a tools_hash.

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
}

// CompactSessionMessage holds a message reference within a session.
type CompactSessionMessage struct {
	Sequence    int       `json:"sequence"`
	MessageHash string    `json:"message_hash"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
	DurationMS  int64     `json:"duration_ms"`
}

type CompactLogger struct {
	db *sql.DB
}

// toolsHash computes a deterministic SHA256 hash of a canonical tools JSON string.
func toolsHash(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// bodyHash computes a deterministic SHA256 hash of a message body.
func bodyHash(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
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

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tools (
			hash    TEXT    PRIMARY KEY,
			body   TEXT    NOT NULL,
			created_at REAL NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			hash       TEXT    PRIMARY KEY,
			body       TEXT    NOT NULL,
			created_at REAL    NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id          TEXT    PRIMARY KEY,
			format      TEXT    NOT NULL,
			model       TEXT    NOT NULL DEFAULT '',
			token_count INTEGER NOT NULL DEFAULT 0,
			tools_hash  TEXT,
			created_at  REAL    NOT NULL,
			FOREIGN KEY(tools_hash) REFERENCES tools(hash)
		)`,
		`CREATE TABLE IF NOT EXISTS session_messages (
			session_id   TEXT    NOT NULL,
			message_hash TEXT    NOT NULL,
			sequence     INTEGER NOT NULL,
			duration_ms  INTEGER NOT NULL DEFAULT 0,
			UNIQUE(session_id, sequence),
			FOREIGN KEY(session_id) REFERENCES sessions(id),
			FOREIGN KEY(message_hash) REFERENCES messages(hash)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_created ON sessions(created_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("compact logger: create schema: %w", err)
		}
	}

	return &CompactLogger{db: db}, nil
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

	var toolsPtr *string
	if toolsHash != "" {
		toolsPtr = &toolsHash
	}
	if _, err := c.db.ExecContext(context.Background(),
		`INSERT INTO sessions (id, format, model, token_count, tools_hash, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, format, model, tokenCount, toolsPtr, time.Now().UnixMilli(),
	); err != nil {
		return "", fmt.Errorf("compact logger: insert session: %w", err)
	}
	return id, nil
}

// StoreTools deduplicates a tools JSON blob, returning its hash.
func (c *CompactLogger) StoreTools(body []byte) string {
	th := toolsHash(body)

	var existing string
	err := c.db.QueryRowContext(context.Background(), "SELECT hash FROM tools WHERE hash = ?", th).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := c.db.ExecContext(context.Background(),
			`INSERT INTO tools (hash, body, created_at) VALUES (?, ?, ?)`,
			th, string(body), time.Now().UnixMilli(),
		); err != nil {
			return ""
		}
		return th
	}
	if err != nil {
		return ""
	}
	return existing
}

// StoreMessage deduplicates a message by body content, returning its hash.
func (c *CompactLogger) StoreMessage(body string) string {
	bh := bodyHash(body)

	var existing string
	err := c.db.QueryRowContext(context.Background(), "SELECT hash FROM messages WHERE hash = ?", bh).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := c.db.ExecContext(context.Background(),
			`INSERT INTO messages (hash, body, created_at) VALUES (?, ?, ?)`,
			bh, body, time.Now().UnixMilli(),
		); err != nil {
			return ""
		}
		return bh
	}
	if err != nil {
		return ""
	}
	return existing
}

// AddMessageToSession adds a message hash to a session's message chain.
func (c *CompactLogger) AddMessageToSession(sessionID, messageHash string, durationMS int64) error {
	var seq int
	err := c.db.QueryRowContext(context.Background(),
		"SELECT COALESCE(MAX(sequence), 0) FROM session_messages WHERE session_id = ?",
		sessionID,
	).Scan(&seq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = c.db.ExecContext(context.Background(),
		`INSERT INTO session_messages (session_id, message_hash, sequence, duration_ms) VALUES (?, ?, ?, ?)`,
		sessionID, messageHash, seq+1, durationMS,
	)
	return err
}

// GetSession reconstructs a conversation, returning messages in order with session metadata.
func (c *CompactLogger) GetSession(id string) (*CompactSession, []CompactSessionMessage, error) {
	// Get session metadata
	var session CompactSession
	var toolsHashPtr *string
	var created float64
	err := c.db.QueryRowContext(context.Background(),
		`SELECT id, format, model, token_count, tools_hash, created_at FROM sessions WHERE id = ?`,
		id,
	).Scan(&session.ID, &session.Format, &session.Model, &session.TokenCount, &toolsHashPtr, &created)
	if err != nil {
		return nil, nil, err
	}
	session.CreatedAt = time.UnixMilli(int64(created))
	session.ToolsHash = toolsHashPtr

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
		var created float64
		if err := rows.Scan(&msg.Sequence, &msg.MessageHash, &msg.Body, &created, &msg.DurationMS); err != nil {
			continue
		}
		msg.CreatedAt = time.UnixMilli(int64(created))
		msgs = append(msgs, msg)
	}
	return &session, msgs, nil
}

// ListSessions returns recent sessions (last n days).
func (c *CompactLogger) ListSessions(days int) ([]CompactSession, error) {
	latestDuration := `(SELECT sm2.duration_ms FROM session_messages sm2 WHERE sm2.session_id = s.id ORDER BY sm2.sequence DESC LIMIT 1)`
	query := `SELECT s.id, s.format, s.model, s.token_count, s.tools_hash, s.created_at, COUNT(sm.message_hash),` + latestDuration + `
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
		var created float64
		var lastDuration *int64
		if err := rows.Scan(&s.ID, &s.Format, &s.Model, &s.TokenCount, &s.ToolsHash, &created, &s.MessageCt, &lastDuration); err != nil {
			continue
		}
		s.CreatedAt = time.UnixMilli(int64(created))
		s.LastDurationMS = lastDuration
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// GetTools retrieves a tools JSON blob by its hash.
func (c *CompactLogger) GetTools(hash string) (string, error) {
	var body string
	err := c.db.QueryRowContext(context.Background(), "SELECT body FROM tools WHERE hash = ?", hash).Scan(&body)
	if err != nil {
		return "", err
	}
	return body, nil
}
