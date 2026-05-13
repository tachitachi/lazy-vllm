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
// Messages are deduplicated via hash-chaining: each message hash is
// SHA256(session_id + prev_hash + body + tools_hash), so the chain is
// implicit in the hashes. Tool definitions are stored in a separate table,
// deduplicated by SHA256(canonical JSON).

// CompactSession summarizes a stored conversation session.
type CompactSession struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	MessageCt int       `json:"message_count"`
}

// CompactMessage represents one message in a conversation chain.
type CompactMessage struct {
	Hash      string    `json:"hash"`
	PrevHash  *string   `json:"prev_hash,omitempty"`
	Body      string    `json:"body"`
	ToolsHash *string   `json:"tools_hash,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type CompactLogger struct {
	db *sql.DB
}

// toolsHash computes a deterministic SHA256 hash of a canonical tools JSON string.
func toolsHash(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// messageHash produces a deterministic hash from session, previous message, body and tools.
func messageHash(sessionID, prevHash, body, toolsHashStr string) string {
	h := sha256.Sum256([]byte(sessionID + prevHash + body + toolsHashStr))
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
			hash   TEXT    PRIMARY KEY,
			body   TEXT    NOT NULL,
			created_at REAL NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id         TEXT    PRIMARY KEY,
			created_at REAL    NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			hash       TEXT    PRIMARY KEY,
			session_id TEXT    NOT NULL,
			prev_hash  TEXT,
			body       TEXT    NOT NULL,
			tools_hash TEXT,
			created_at REAL    NOT NULL,
			FOREIGN KEY(session_id) REFERENCES sessions(id),
			FOREIGN KEY(tools_hash) REFERENCES tools(hash)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_created ON messages(created_at DESC)`,
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

// StartSession creates a new session, returning the session ID.
func (c *CompactLogger) StartSession() (string, error) {
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

	if _, err := c.db.ExecContext(context.Background(),
		`INSERT INTO sessions (id, created_at) VALUES (?, ?)`,
		id, time.Now().UnixMilli(),
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

// StoreMessage inserts a message into the chain, deduplicating by hash.
// prevHash is the parent message hash (empty string for first message).
// toolsHashStr is the tools hash (empty string if no tools).
func (c *CompactLogger) StoreMessage(sessionID, prevHash, body, toolsHashStr string) *CompactMessage {
	mh := messageHash(sessionID, prevHash, body, toolsHashStr)

	var existing string
	err := c.db.QueryRowContext(context.Background(), "SELECT hash FROM messages WHERE hash = ?", mh).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		var prevPtr *string
		if prevHash != "" {
			prevPtr = &prevHash
		}
		var toolsPtr *string
		if toolsHashStr != "" {
			toolsPtr = &toolsHashStr
		}
		if _, err := c.db.ExecContext(context.Background(),
			`INSERT INTO messages (hash, session_id, prev_hash, body, tools_hash, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			mh, sessionID, prevPtr, body, toolsPtr, time.Now().UnixMilli(),
		); err != nil {
			return nil
		}
	} else if err != nil {
		return nil
	}

	// For returned record, use pointers for nullable fields.
	var prevPtr *string
	if prevHash != "" {
		prevPtr = &prevHash
	}
	var toolsPtr *string
	if toolsHashStr != "" {
		toolsPtr = &toolsHashStr
	}
	return &CompactMessage{
		Hash:      mh,
		PrevHash:  prevPtr,
		Body:      body,
		ToolsHash: toolsPtr,
		CreatedAt: time.Now(),
	}
}

// GetSession reconstructs a full conversation from the message chain.
func (c *CompactLogger) GetSession(id string) ([]CompactMessage, error) {
	rows, err := c.db.QueryContext(context.Background(),
		`SELECT hash, prev_hash, body, tools_hash, created_at
		 FROM messages WHERE session_id = ? ORDER BY created_at ASC`,
		id,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var msgs []CompactMessage
	for rows.Next() {
		var msg CompactMessage
		var created float64
		if err := rows.Scan(&msg.Hash, &msg.PrevHash, &msg.Body, &msg.ToolsHash, &created); err != nil {
			continue
		}
		msg.CreatedAt = time.UnixMilli(int64(created))
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

// ListSessions returns recent sessions (last n days).
func (c *CompactLogger) ListSessions(days int) ([]CompactSession, error) {
	cutoff := time.Now().AddDate(0, 0, -days).UnixMilli()
	rows, err := c.db.QueryContext(context.Background(),
		`SELECT s.id, s.created_at, COUNT(m.hash)
		 FROM sessions s
		 LEFT JOIN messages m ON m.session_id = s.id
		 WHERE s.created_at >= ?
		 GROUP BY s.id
		 ORDER BY s.created_at DESC`,
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var sessions []CompactSession
	for rows.Next() {
		var s CompactSession
		var created float64
		if err := rows.Scan(&s.ID, &created, &s.MessageCt); err != nil {
			continue
		}
		s.CreatedAt = time.UnixMilli(int64(created))
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
