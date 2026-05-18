# ChromaDB Session Exclusion: Preventing Self-Reference

## The Problem

lazy-vllm's CompactLogger creates sessions per HTTP request via `StartSession()`. Unlike claude-mem/claude-brain, the proxy doesn't know Claude Code's session UUID. When querying Chroma for "related message history," there's a risk of pulling back messages already in the current session's context window — wasting tokens and creating redundancy.

### Why lazy-vllm Is Different

| Property | claude-mem | lazy-vllm |
|----------|-----------|-----------|
| Session ID | Claude Code session UUID (known, stable across turns) | CompactLogger-generated UUID (new per HTTP request) |
| Capture point | IDE hooks (PostToolUse, SessionEnd) | HTTP proxy middleware (every request) |
| Dedup | SHA256(session_id + title + narrative) | SHA256(message body) — global across sessions |
| Chroma metadata | `memory_session_id` maps to IDE session | No concept of "conversation" — just request/response pairs |

A single Claude Code turn can produce 10+ proxy calls (tool reads, edits, bash commands). Each one gets its own CompactLogger session. Chroma has no way to distinguish "this message is already in your context window" from "this is genuinely related history from last week."

## What We Know at Query Time

1. **`session_id`** — `StartSession()` returns a UUID, known immediately
2. **Request timestamp** — `created_at_epoch` on each message
3. **In-memory process state** — The proxy is a long-lived Go process
4. **Message content** — Current request's messages are in the HTTP body (hashable)

---

## Five-Layer Defense-in-Depth Strategy

### Layer 1: Exclude Current Session ID (Covers ~80%)

At Chroma query time, exclude the active session:

```go
// Chroma where clause
where := map[string]interface{}{
    "$ne": map[string]interface{}{
        "session_id": currentSessionID,
    },
}
```

Chroma supports `$ne` and `$and`. Single-session exclusion handles the obvious case — don't search back your own request.

### Layer 2: Ring Buffer of Recent Session IDs (Covers ~95%)

The proxy is a long-lived Go process. Track recent session IDs in a fixed-size ring buffer:

```go
type recentSessionTracker struct {
    mu    sync.Mutex
    ring  [64]string // O(1) max, 64 concurrent requests
    head  int
    count int
}

func (t *recentSessionTracker) Add(id string) {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.ring[t.head] = id
    t.head = (t.head + 1) % len(t.ring)
    if t.count < len(t.ring) {
        t.count++
    }
}

func (t *recentSessionTracker) Last(n int) []string {
    t.mu.Lock()
    defer t.mu.Unlock()
    // Return last N IDs (most recent first)
    // ...
}
```

At Chroma query time, build an `$and` clause with `$ne` for the last ~20 session IDs. A typical Claude Code turn generates 5-10 proxy calls (tool calls, observations, chat completions). Excluding 20 gives plenty of margin for multi-turn conversations.

**Cost:** ~20 `$ne` clauses in the Chroma `where` filter, O(1) Go memory (64 × 36 bytes = 2.3KB).

### Layer 3: Timestamp Floor — Safety Net (Covers ~99%)

For edge cases (ring buffer wrap, process restart), exclude messages from the last N minutes:

```go
excludeBefore := time.Now().Add(-2 * time.Minute).UnixMilli()
```

Combined with session ID exclusion in the Chroma `where` clause. A 2-minute floor is conservative — a Claude Code turn with multiple tool calls can take 30-60 seconds. The worst case is missing genuinely relevant history from 90 seconds ago, which is already in context anyway.

**Process restart gap:** The ring buffer is in-memory, so restart = lost state. The timestamp floor handles this — anything from the last 2 minutes is excluded regardless. For extra durability, persist the last 64 session IDs to a small file (~1KB) and reload on startup.

### Layer 4: Content-Hash Dedup — Absolute Guarantee

After Chroma returns results, deduplicate against the current request's message hashes:

```go
// SHA256 of each message body in the current request
currentHashes := make(map[string]struct{})
for _, msg := range currentMessages {
    currentHashes[sha256(msg.Body)] = struct{}{}
}

// Filter Chroma results
filtered := filter(results, func(doc) bool {
    _, exists := currentHashes[doc.Metadata["message_hash"]]
    return !exists // Keep only non-duplicates
})
```

**Cost:** O(K × M) where K = Chroma results (~10-20) and M = current messages. Negligible. This is the same SHA256 hash already computed for CompactLogger dedup.

### Layer 5: Similarity Threshold — Self-Match Guard

If the current query is embedded alongside historical messages, a query could match its own embedding. Filter by cosine similarity:

```go
// If a result has >0.95 similarity to the query,
// it's likely the query itself or a near-duplicate
if distance > 0.95 {
    continue // skip
}
```

Optional but handles the degenerate case where the system just embedded the current message and immediately searched for it.

---

## The Full Query Flow

```
Client Request arrives at proxy
    │
    ├── forwardWithLogging()
    │   ├── StartSession() → new session_id
    │   ├── StoreMessage(input messages) [CompactLogger]
    │   ├── recentSessionTracker.Add(session_id)
    │   │
    │   ├── [NEW] Chroma: embed input messages
    │   │   with session_id, message_hash, timestamp metadata
    │   │
    │   └── forwardRequest() → upstream vLLM
    │
    └── [NEW] Chroma: query for related history
        ├── where: session_id NOT IN recentSessionTracker.Last(20)
        ├── where: created_at_epoch < now() - 2min
        ├── Chroma returns top-K results
        └── Filter: exclude any result whose message_hash
              exists in current request's message hashes
```

---

## Chroma Document Schema

```go
type ChromaMessageDoc struct {
    ID       string // "msg_{session_id}_{sequence}"
    Document string // message body text
    Metadata map[string]any{
        "session_id":       sessionUUID,         // for session exclusion
        "message_hash":      sha256(body),       // for content dedup
        "message_type":      "user" | "assistant" | "system" | "tool",
        "format":           "openai" | "anthropic",
        "model":            "qwen3-8b-flash",
        "created_at_epoch":  timestamp,          // for recency floor
        "tools_hash":       toolsHash,           // if tools were present
        "duration_ms":       latency,           // for assistant messages
    }
}
```

### Chroma Where Clause Construction

```go
func buildExclusionFilter(sessionID string, recentTracker *recentSessionTracker, timestampFloor int64) map[string]any {
    recentIDs := recentTracker.Last(20)

    conditions := []map[string]any{
        {"$ne": map[string]any{"session_id": sessionID}},
        {"created_at_epoch": map[string]any{"$lt": timestampFloor}},
    }

    for _, rid := range recentIDs {
        conditions = append(conditions, map[string]any{
            "$ne": map[string]any{"session_id": rid},
        })
    }

    return map[string]any{
        "$and": conditions,
    }
}
```

---

## Layer Effectiveness Matrix

| Layer | Mechanism | Covers | Cost | Failure Mode |
|-------|-----------|--------|------|-------------|
| 1 | Exclude current session ID | 80% (single request) | Free — one `$ne` clause | Multi-turn sessions (same conversation across requests) |
| 2 | Ring buffer of recent IDs | 95% (multi-turn) | ~20 `$ne` clauses, O(1) memory | Process restart (ring is lost) |
| 3 | Timestamp floor (2 min) | ~99% (restart edge case) | One `$gt` clause | Genuinely fast request from 90s ago (harmless) |
| 4 | Content-hash dedup | 100% (absolute) | O(K×M) filter on ~10 results | None — mathematically guaranteed |
| 5 | Similarity threshold | Degenerate self-match | One float comparison | Near-duplicate that's actually relevant (>0.95 sim) |

---

## The Key Insight

**You don't need to know about Claude Code sessions at all.** You just need to answer: *"What did this proxy process see in the last few minutes?"* — and that's pure proxy state, no external coordination needed.

The combination of layers 1-3 handles ~99% of cases with zero external state. Layer 4 provides a mathematically guaranteed safety net for the remaining 1%. Together they cost virtually nothing but eliminate self-reference entirely.

### If You Could Only Pick Two

- **Layer 2** (ring buffer) — handles multi-turn conversations with minimal cost
- **Layer 4** (hash dedup) — guarantees no duplicates even in edge cases

Everything else is optimization and belt-and-suspenders.

---

## Implementation Order

1. **CompactLogger metadata** — Add `session_id`, `message_hash`, `created_at_epoch` to Chroma document metadata (one-time schema work)
2. **Ring buffer** — In-proxy `recentSessionTracker` (pure Go, no external deps)
3. **Where clause builder** — Compose Chroma `where` filter with session exclusion + timestamp floor
4. **Post-query hash filter** — O(K×M) dedup against current request messages
5. **Similarity threshold** — Optional, last safeguard

## Relationship to Existing Architecture

This fits naturally into `forwardWithLogging()` in `proxy/internal/proxy/proxy.go`:

```
extractTools() → StoreTools() → StartSession()
    ├── recentSessionTracker.Add(sessionID)       // NEW
    ├── StoreMessage() + AddMessageToSession()    // EXISTING
    ├── ChromaSync.syncMessages()                // NEW
    ├── forwardRequest()                          // EXISTING
    └── ChromaSearch + hashDedup()               // NEW (optional, before response)
```

The Chroma integration would live alongside the existing `DiskLogger` and `CompactLogger` as a third logging path — `ChromaLogger` — that syncs messages to Chroma and provides search at proxy time.
