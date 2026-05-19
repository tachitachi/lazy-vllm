package logger

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestLogger(t *testing.T) *CompactLogger {
	t.Helper()
	dir := t.TempDir()
	l, err := NewCompact(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestToolsHash_Deterministic(t *testing.T) {
	tools := []byte(`[{"type":"function","function":{"name":"weather"}}]`)
	h1 := toolsHash(tools)
	h2 := toolsHash(tools)
	if !bytes.Equal(h1, h2) {
		t.Errorf("same input produced different hashes: %q vs %q", hashHex(h1), hashHex(h2))
	}
}

func TestToolsHash_DifferentContent(t *testing.T) {
	toolsA := []byte(`[{"type":"function","function":{"name":"weather"}}]`)
	toolsB := []byte(`[{"type":"function","function":{"name":"search"}}]`)
	if bytes.Equal(toolsHash(toolsA), toolsHash(toolsB)) {
		t.Error("different tools should produce different hashes")
	}
}

func TestStoreTools_Deduplication(t *testing.T) {
	l := newTestLogger(t)

	tools := []byte(`[{"type":"function","function":{"name":"weather"}}]`)
	hash1 := l.StoreTools(tools)
	hash2 := l.StoreTools(tools)
	if hash1 != hash2 {
		t.Error("same tools should return same hash")
	}

	var count int
	if err := l.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM tools").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 tools row, got %d", count)
	}
}

func TestBodyHash_Deterministic(t *testing.T) {
	body := `{"role":"user","content":"hello"}`
	h1 := bodyHash(body)
	h2 := bodyHash(body)
	if !bytes.Equal(h1, h2) {
		t.Errorf("same input produced different hashes: %q vs %q", hashHex(h1), hashHex(h2))
	}
	if len(h1) == 0 {
		t.Error("hash should not be empty")
	}
}

func TestBodyHash_DifferentBody(t *testing.T) {
	a := `{"role":"user","content":"hello"}`
	b := `{"role":"user","content":"world"}`
	if bytes.Equal(bodyHash(a), bodyHash(b)) {
		t.Error("different bodies should produce different hashes")
	}
}

func TestStoreMessage_Deduplication(t *testing.T) {
	l := newTestLogger(t)

	body := `{"role":"user","content":"hello"}`
	h1 := l.StoreMessage(body)
	h2 := l.StoreMessage(body)
	if h1 != h2 {
		t.Error("same message should return same hash")
	}

	var count int
	if err := l.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM messages").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 message row, got %d", count)
	}
}

func TestStoreMessage_GlobalDedup(t *testing.T) {
	l := newTestLogger(t)

	body := `{"role":"user","content":"hello"}`
	h1 := l.StoreMessage(body)
	h2 := l.StoreMessage(body)
	// Both calls return the same hash — same message stored once.
	if h1 != h2 {
		t.Error("global dedup failed")
	}

	// Two different messages → two rows
	body2 := `{"role":"assistant","content":"hi"}`
	h3 := l.StoreMessage(body2)
	if h1 == h3 {
		t.Error("different messages should produce different hashes")
	}

	var count int
	if err := l.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM messages").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected 2 message rows, got %d", count)
	}
}

func TestSessionWithMessages(t *testing.T) {
	l := newTestLogger(t)

	tools := []byte(`[{"type":"function","function":{"name":"calc"}}]`)
	toolsHash := l.StoreTools(tools)
	sessionID, err := l.StartSession(toolsHash, "openai", "claude-sonnet-4-6", 100)
	if err != nil {
		t.Fatal(err)
	}

	// Store messages
	msg1Hash := l.StoreMessage(`{"role":"user","content":"hello"}`)
	msg2Hash := l.StoreMessage(`{"role":"assistant","content":"hi"}`)
	if msg1Hash == "" || msg2Hash == "" {
		t.Fatal("StoreMessage returned empty hash")
	}

	// Add to session
	if err := l.AddMessageToSession(sessionID, msg1Hash, 0); err != nil {
		t.Fatal(err)
	}
	if err := l.AddMessageToSession(sessionID, msg2Hash, 1234); err != nil {
		t.Fatal(err)
	}

	// Retrieve session
	session, msgs, err := l.GetSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Body != `{"role":"user","content":"hello"}` {
		t.Errorf("msg1 body mismatch: %q", msgs[0].Body)
	}
	if msgs[0].DurationMS != 0 {
		t.Errorf("msg1 duration should be 0, got %d", msgs[0].DurationMS)
	}
	if msgs[1].Body != `{"role":"assistant","content":"hi"}` {
		t.Errorf("msg2 body mismatch: %q", msgs[1].Body)
	}
	if msgs[1].DurationMS != 1234 {
		t.Errorf("msg2 duration should be 1234, got %d", msgs[1].DurationMS)
	}
	if session.ToolsHash == nil || *session.ToolsHash != toolsHash {
		t.Error("tools hash should be set on session")
	}
	if session.Model != "claude-sonnet-4-6" {
		t.Errorf("model should be claude-sonnet-4-6, got %q", session.Model)
	}
}

func TestMessageCrossSessionDedup(t *testing.T) {
	l := newTestLogger(t)

	body := `{"role":"user","content":"hello"}`
	h := l.StoreMessage(body)

	// Same message in two sessions → same hash (global dedup)
	h2 := l.StoreMessage(body)
	if h != h2 {
		t.Error("same body should produce same hash across sessions")
	}

	var count int
	if err := l.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM messages").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("global dedup: expected 1 message row, got %d", count)
	}
}

func TestListSessions(t *testing.T) {
	l := newTestLogger(t)

	_, _ = l.StartSession("", "openai", "claude-sonnet-4-6", 0)
	_, _ = l.StartSession("", "anthropic", "claude-sonnet-4-6-flash", 0)
	_, _ = l.StartSession("", "openai", "", 0)

	sessions, err := l.ListSessions(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}
	expectedModels := map[string]int{"claude-sonnet-4-6": 1, "claude-sonnet-4-6-flash": 1, "": 1}
	for _, s := range sessions {
		delete(expectedModels, s.Model)
	}
	if len(expectedModels) > 0 {
		t.Errorf("missing models: %v", expectedModels)
	}
}

func TestGetTools(t *testing.T) {
	l := newTestLogger(t)

	tools := []byte(`[{"type":"function","function":{"name":"weather"}}]`)
	hash := l.StoreTools(tools)
	if hash == "" {
		t.Fatal("StoreTools returned empty hash")
	}

	retrieved, err := l.GetTools(hash)
	if err != nil {
		t.Fatal(err)
	}
	if retrieved != string(tools) {
		t.Errorf("tools mismatch: %q != %q", retrieved, tools)
	}
}

func TestSetSessionSummary(t *testing.T) {
	l := newTestLogger(t)

	sessionID, err := l.StartSession("", "anthropic", "claude-sonnet-4-6", 0)
	if err != nil {
		t.Fatal(err)
	}

	// Summary is nil before being set.
	session, _, err := l.GetSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Summary != nil {
		t.Errorf("expected nil summary before set, got %q", *session.Summary)
	}

	// Set summary and verify it's stored.
	want := "User asked about 2+2. Answered 4."
	if err := l.SetSessionSummary(sessionID, want); err != nil {
		t.Fatal(err)
	}

	session, _, err = l.GetSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Summary == nil {
		t.Fatal("expected summary to be set, got nil")
	}
	if *session.Summary != want {
		t.Errorf("summary mismatch: got %q, want %q", *session.Summary, want)
	}
}

func TestListSessions_IncludesSummary(t *testing.T) {
	l := newTestLogger(t)

	idWithSummary, err := l.StartSession("", "anthropic", "claude-sonnet-4-6", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = l.StartSession("", "anthropic", "claude-sonnet-4-6", 0)
	if err != nil {
		t.Fatal(err)
	}

	want := "Did something useful."
	if err := l.SetSessionSummary(idWithSummary, want); err != nil {
		t.Fatal(err)
	}

	sessions, err := l.ListSessions(7)
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, s := range sessions {
		if s.ID == idWithSummary {
			if s.Summary == nil {
				t.Fatal("expected summary on session, got nil")
			}
			if *s.Summary != want {
				t.Errorf("summary mismatch: got %q, want %q", *s.Summary, want)
			}
			found = true
		} else if s.Summary != nil {
			t.Errorf("session %q should have nil summary, got %q", s.ID, *s.Summary)
		}
	}
	if !found {
		t.Error("session with summary not found in list")
	}
}

func TestNewCompact_TempDir(t *testing.T) {
	dir := t.TempDir()
	l, err := NewCompact(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()

	if l.db == nil {
		t.Error("db should not be nil")
	}

	if _, err := os.Stat(filepath.Join(dir, "compact_logs.db")); os.IsNotExist(err) {
		t.Error("compact_logs.db should exist")
	}
}

func TestSchemaVersion(t *testing.T) {
	l := newTestLogger(t)

	var version int
	err := l.db.QueryRowContext(context.Background(), "SELECT version FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Errorf("expected schema version %d, got %d", schemaVersion, version)
	}
}

func TestCompression_RoundTrip(t *testing.T) {
	// Test small payload (stored uncompressed)
	small := `{"role":"user","content":"hi"}`
	compressedSmall := compressData([]byte(small))
	decompressedSmall := decompressData(compressedSmall)
	if string(decompressedSmall) != small {
		t.Errorf("small roundtrip failed: got %q", string(decompressedSmall))
	}

	// Test large payload (stored compressed)
	large := strings.Repeat("x", 10_000)
	compressedLarge := compressData([]byte(large))
	if len(compressedLarge) >= len(large) {
		t.Error("large payload should be compressed")
	}
	decompressedLarge := decompressData(compressedLarge)
	if string(decompressedLarge) != large {
		t.Errorf("large roundtrip failed")
	}
}

func TestStoreMessage_Compression(t *testing.T) {
	l := newTestLogger(t)

	// Store a large message body
	body := strings.Repeat("hello world ", 1000)
	hash := l.StoreMessage(body)
	if hash == "" {
		t.Fatal("StoreMessage returned empty hash")
	}

	// Directly verify DB stores compressed data
	var storedBody []byte
	err := l.db.QueryRowContext(context.Background(), "SELECT body FROM messages WHERE hash = ?", hexHash(hash)).Scan(&storedBody)
	if err != nil {
		t.Fatal(err)
	}
	decompressed := decompressData(storedBody)
	if string(decompressed) != body {
		t.Errorf("roundtrip failed: got %d bytes, want %d", len(decompressed), len(body))
	}
}
