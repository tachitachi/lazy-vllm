package logger

import (
	"context"
	"os"
	"path/filepath"
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

func TestComputeMessageHash_Deterministic(t *testing.T) {
	h1 := messageHash("s1", "prev", "body", "")
	h2 := messageHash("s1", "prev", "body", "")
	if h1 != h2 {
		t.Errorf("same input produced different hashes: %q vs %q", h1, h2)
	}
	if len(h1) == 0 {
		t.Error("hash should not be empty")
	}
}

func TestComputeMessageHash_DifferentSession(t *testing.T) {
	h1 := messageHash("s1", "prev", "body", "")
	h2 := messageHash("s2", "prev", "body", "")
	if h1 == h2 {
		t.Error("different sessions should produce different hashes")
	}
}

func TestComputeMessageHash_DifferentBody(t *testing.T) {
	h1 := messageHash("s1", "prev", "body-a", "")
	h2 := messageHash("s1", "prev", "body-b", "")
	if h1 == h2 {
		t.Error("different bodies should produce different hashes")
	}
}

func TestToolsHash_Deterministic(t *testing.T) {
	tools := []byte(`[{"type":"function","function":{"name":"weather"}}]`)
	h1 := toolsHash(tools)
	h2 := toolsHash(tools)
	if h1 != h2 {
		t.Errorf("same input produced different hashes: %q vs %q", h1, h2)
	}
}

func TestToolsHash_DifferentContent(t *testing.T) {
	toolsA := []byte(`[{"type":"function","function":{"name":"weather"}}]`)
	toolsB := []byte(`[{"type":"function","function":{"name":"search"}}]`)
	h1 := toolsHash(toolsA)
	h2 := toolsHash(toolsB)
	if h1 == h2 {
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

func TestStoreMessage_Chain(t *testing.T) {
	l := newTestLogger(t)

	sessionID, err := l.StartSession()
	if err != nil {
		t.Fatal(err)
	}

	msg1Body := `{"role":"user","content":"hello"}`
	msg2Body := `{"role":"assistant","content":"hi there"}`
	msg3Body := `{"role":"user","content":"how are you"}`

	msg1 := l.StoreMessage(sessionID, "", msg1Body, "")
	if msg1 == nil {
		t.Fatal("StoreMessage returned nil for msg1")
	}
	msg2 := l.StoreMessage(sessionID, msg1.Hash, msg2Body, "")
	if msg2 == nil {
		t.Fatal("StoreMessage returned nil for msg2")
	}
	msg3 := l.StoreMessage(sessionID, msg2.Hash, msg3Body, "")
	if msg3 == nil {
		t.Fatal("StoreMessage returned nil for msg3")
	}

	if msg1.PrevHash != nil {
		t.Error("first message should have nil prev_hash")
	}
	if msg2.PrevHash == nil || *msg2.PrevHash != msg1.Hash {
		t.Error("second message should link to first")
	}
	if msg3.PrevHash == nil || *msg3.PrevHash != msg2.Hash {
		t.Error("third message should link to second")
	}
}

func TestStoreMessage_Deduplication(t *testing.T) {
	l := newTestLogger(t)

	sessionID, _ := l.StartSession()
	body := `{"role":"user","content":"hello"}`

	msg1 := l.StoreMessage(sessionID, "", body, "")
	msg2 := l.StoreMessage(sessionID, "", body, "")

	if msg1.Hash != msg2.Hash {
		t.Error("same message should produce same hash")
	}

	var count int
	if err := l.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM messages").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 message row, got %d", count)
	}
}

func TestGetSession_Reconstruction(t *testing.T) {
	l := newTestLogger(t)

	sessionID, _ := l.StartSession()

	l.StoreMessage(sessionID, "", `{"role":"user","content":"hello"}`, "")
	l.StoreMessage(sessionID, "prev1", `{"role":"assistant","content":"hi"}`, "")
	l.StoreMessage(sessionID, "prev2", `{"role":"user","content":"how are you"}`, "")

	msgs, err := l.GetSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
}

func TestCrossSessionIsolation(t *testing.T) {
	l := newTestLogger(t)

	body := `{"role":"user","content":"hello"}`

	id1, _ := l.StartSession()
	id2, _ := l.StartSession()

	msg1 := l.StoreMessage(id1, "", body, "")
	msg2 := l.StoreMessage(id2, "", body, "")

	if msg1.Hash == msg2.Hash {
		t.Error("same body in different sessions should produce different hashes")
	}
}

func TestListSessions(t *testing.T) {
	l := newTestLogger(t)

	_, _ = l.StartSession()
	_, _ = l.StartSession()
	_, _ = l.StartSession()

	sessions, err := l.ListSessions(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
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
