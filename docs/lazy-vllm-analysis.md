# lazy-vllm: Architecture Analysis

## Project Overview

**lazy-vllm** is a local LLM infrastructure stack built around Qwen models, providing a routing proxy, observability, logging, and a sandboxed Claude Code environment via Docker Compose. Written primarily in Go.

**Key design goals:** Smart routing across multiple vLLM backends, full request/response capture with web UI, sandboxed Claude Code execution against local models, single-command deployment.

---

## Architecture

### Capture Layer (Proxy Logging)

The proxy implements **two parallel logging systems** that capture all LLM interactions at the HTTP proxy level.

#### System 1: DiskLogger (Full Request/Response Logs)

**File:** `proxy/internal/logger/logger.go`

Captures every complete request with full call chain details.

**Storage:** SQLite (`agent-logs/logs.db`) via pure-Go `modernc.org/sqlite` driver, WAL mode enabled.

**Schema:**

```sql
CREATE TABLE requests (
    id              TEXT    PRIMARY KEY,   -- UUIDv4
    started_at      INTEGER NOT NULL,      -- Unix ms
    finished_at     INTEGER NOT NULL DEFAULT 0,
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    format          TEXT    NOT NULL,       -- "openai" or "anthropic"
    request_path    TEXT, request_headers TEXT (JSON),
    request_body    TEXT (full JSON),
    calls            TEXT (JSON array of CallLog)
);
```

- `RequestLog`: Top-level entry (id, timestamps, format, path, headers, body, calls[])
- `CallLog`: Per-model-call details (node_id, node_kind, iteration, timestamp, input_messages[], output{content, reasoning, tool_calls})
- Migration via `PRAGMA user_version` (currently v0 → v1)

**HTTP endpoints:**
- `GET /api/logs` — List recent logs (last 7 days) as `LogSummary[]`
- `GET /api/logs/{id}` — Full `RequestLog` JSON
- `GET /ui/logs` — Embedded HTML web UI

#### System 2: CompactLogger (Deduplicated Session Storage)

**File:** `proxy/internal/logger/compact.go`

Deduplicated session-based storage: O(n) across sessions via SHA256 content hashing.

**Storage:** SQLite (`agent-logs/compact_logs.db`), WAL mode.

**Schema:**

```sql
CREATE TABLE tools (
    hash    TEXT    PRIMARY KEY,    -- SHA256 of canonical JSON tools blob
    body   TEXT    NOT NULL,
    created_at REAL NOT NULL
);

CREATE TABLE messages (
    hash       TEXT    PRIMARY KEY,  -- SHA256 of canonical JSON message body
    body       TEXT    NOT NULL,
    created_at REAL    NOT NULL
);

CREATE TABLE sessions (
    id          TEXT    PRIMARY KEY,
    format      TEXT    NOT NULL,
    model       TEXT    NOT NULL DEFAULT '',
    token_count INTEGER NOT NULL DEFAULT 0,
    tools_hash  TEXT REFERENCES tools(hash),
    created_at  REAL    NOT NULL
);

CREATE TABLE session_messages (
    session_id   TEXT REFERENCES sessions(id),
    message_hash TEXT REFERENCES messages(hash),
    sequence     INTEGER NOT NULL,
    duration_ms  INTEGER NOT NULL DEFAULT 0,
    UNIQUE(session_id, sequence)
);
```

**Key design features:**

1. **Global message deduplication:** `bodyHash(body) = SHA256(body)` — identical message bodies across all sessions share one row. Verified by `TestStoreMessage_GlobalDedup`.
2. **Tool deduplication:** `toolsHash(body) = SHA256(canonical_tools_json)` — stored once globally.
3. **Session referencing:** `session_messages` join table stores only hash + sequence number, not full content.
4. **Timing metadata:** `duration_ms` per message (0 for input, upstream latency for output).

**Data flow in `forwardWithLogging()`:**
1. Extract tools → `StoreTools()` → deduplicated, get `toolsHash`
2. `StartSession(toolsHash, format, model, tokenCount)` → returns `sessionID`
3. Parse input messages → `StoreMessage()` + `AddMessageToSession()` for each
4. Forward to upstream vLLM, capture response
5. Parse response → `StoreMessage()` + `AddMessageToSession()` with `durationMS`

### Message Parsing (`parse.go`)

Dedicated parsers for both API formats:

**OpenAI format:**
- `ParseMessages()` — Extracts `messages[]` from request body
- `ParseOpenAIOutput()` — Parses streaming (SSE) and non-streaming responses; extracts content, reasoning, tool_calls

**Anthropic format:**
- `ParseAnthropicMessages()` — Converts `system` + `messages` (with content blocks: tool_use, tool_result, thinking, text) into OpenAI-compatible `[]Message`. Merges thinking blocks into `reasoningContent`. Handles nested tool_result sub-blocks.
- `ParseAnthropicOutput()` — Parses content stream events (message_start, content_block_start, content_block_delta, message_delta) with text_delta, thinking_delta, input_json_delta support.

### Agent Graph Framework (In-Memory Context Management)

#### Core Types (`types.go`)
- `PipelineCtx` — Mutable state through the graph: `Messages []ChatMessage` (cross-turn history), `NodeOutputs`, `ThinkingMode`, `ModelOverride`
- `ChatMessage` — Role, Content (string or []ContentBlock), ReasoningContent, ToolCalls, ToolCallID, Name
- `HistoryPolicy` — `StripThinkingOnCommit bool`, `WindowSize int` (0 = unlimited)
- `Agent` — System prompt, model, max tokens, thinking mode, tools, history policy
- `Graph` — Nodes, scatter config, edges, entry point

#### History Management (`history.go`)
- `StripAllThinking(msgs)` — Removes thinking content from all messages
- `StripMessageThinking(m)` — Strips reasoning from single message. Handles multiple formats: `ReasoningContent` field, `[]any` content blocks (filters out thinking/redacted_thinking), `[]ContentBlock`, `json.RawMessage`
- `applyWindow(msgs, n)` — Sliding window of last n messages (n=0 → no limit)
- `PipelineCtx.DeepCopy()` — Independent copy for scatter branches

#### Thinking Rules (Gemma4 compliance):
1. Strip thinking from prior assistant turns — `pctx.Messages` always holds stripped history
2. Tool call exception — Within a single Chain node's tool loop, thinking is NOT stripped between iterations
3. Turn commit — When Chain node completes, final assistant message is stripped before appending to `pctx.Messages`

#### Executor (`executor.go`):
- `runRoute()` — Single model call, captures string output, does NOT modify `pctx.Messages`
- `runChain()` — Tool loop with internal `workingMessages`. Strips thinking only on final commit.
- `runScatter()` — Parallel fan-out via `errgroup`, each branch gets deep copy, results merged via configurable merge function
- `runRespond()` — Rebuilds request from `pctx.Messages`, proxies to vLLM, streams response. Handles Anthropic tool grouping (consecutive tool messages into single user turn).

#### Anthropic Format Conversion (`anthropic.go`):
- `AnthropicToChatMessages()` — Anthropic API → internal `[]ChatMessage`
- `BuildAnthropicRequestBody()` — Internal `[]ChatMessage` → Anthropic format for proxying
- `chatMessagesToAnthropic()` — Groups consecutive tool messages into single Anthropic user message

### Agent Disk Logger (File-Based, Separate from Proxy)

**File:** `agent/internal/agent/disk_logger.go`

Stores logs as individual JSON files: `{logDir}/{date}/{uuid}.json` (e.g., `agent-logs/2026-05-09/146bbd19-...json`)

- Date-based subdirectory structure
- `ListLogs()` — Scans last N days, returns summaries sorted by time
- `GetLog()` — Searches date directories for specific ID

### Retrieval

**No embedding/vector search.** The system has no semantic search, no vector database, and no memory indexing layer. History retrieval relies on:

1. **Exact deduplication** — SHA256 hash of message bodies for global message dedup
2. **Session-based lookup** — UUID-keyed session retrieval for full conversation replay
3. **Sliding window** — Configurable `WindowSize` in `HistoryPolicy`
4. **Time-based browsing** — Date-partitioned file logs, 7-day default query window

### Graph Registry

Two pre-configured graphs:
1. **"thinking-router"** — Classify message as DIRECT or REASONING (structured output), route with appropriate thinking mode
2. **"assistant"** — Simple single-turn assistant

Both use `HistoryPolicy{StripThinkingOnCommit: true}`.

### End-to-End Data Flow

```
Client Request (OpenAI/Anthropic format)
    |
    v
[handlers.go] forwardWithLogging()
    |
    +---> CompactLogger: StoreTools() [if tools present]
    |     StoreMessage() for each input message
    |     AddMessageToSession() to session chain
    |
    +---> DiskLogger.Start()
    |
    +---> ResponseCapture wraps response writer
    |
    +---> forwardRequest() → upstream vLLM
    |
    +---> DiskLogger.Save() with call chain
    |     StoreMessage() for assistant output
    |     AddMessageToSession() with duration
    |
    +---> Response streamed to client
    |
    v
[Web UI] GET /api/logs, /api/sessions, /api/tools
```

### Design Summary

| Aspect | Approach |
|--------|----------|
| Capture | HTTP proxy middleware (captures all request/response at wire level) |
| Granularity | Full request/response (DiskLogger) + session-level message chains (CompactLogger) |
| Dedup | SHA256 content hash on message bodies (global dedup across sessions) |
| Storage | Dual SQLite: `logs.db` (full logs) + `compact_logs.db` (deduplicated sessions) + JSON files |
| Indexing | SHA256 content hash (exact dedup only) |
| Search | None — UUID/time-based lookup, no keyword or semantic search |
| Retrieval | Session replay, sliding window, 7-day time-range browsing via web UI |
| Embedding | **None** — no vector search, no semantic similarity |
| Scope | Infrastructure proxy (routing, logging, observability), not a memory system |
| Format Handling | Dual-format parsers for OpenAI and Anthropic APIs (streaming + non-streaming) |
