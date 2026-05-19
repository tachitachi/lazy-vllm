# Memory Service

Semantic memory for lazy-vllm. Every LLM session is summarized into a one-sentence observation, embedded with `all-MiniLM-L6-v2`, and stored in ChromaDB. An MCP server exposes the embeddings to Claude Code so it can recall relevant past sessions by semantic similarity.

## How it works

```
Claude Code ──POST /v1/messages──▶ Proxy (Go)
                                        │
                              inject <obs> directive
                              into system prompt
                                        │
                                        ▼
                                  Anthropic / vLLM
                                        │
                              response contains
                              <obs>...</obs> block
                                        │
                              strip block from
                              response to client
                                        │
                              ┌─────────┴──────────┐
                              ▼                    ▼
                          SQLite               memory service
                       sessions.summary      POST /ingest
                                                   │
                                             ChromaDB upsert
                                             (session_id, summary,
                                              model, format,
                                              token_count)

Claude Code ◀──MCP SSE──── Memory Service
  search_memories(query)    query ChromaDB
                            cosine similarity
                            return top-N sessions
```

### Observation extraction

The proxy injects a directive into every system prompt:

```
After your response, output exactly one <obs> block:
<obs>1–3 sentences: what was asked, what you did, any key files changed.</obs>
```

The proxy's response capture layer strips the `<obs>...</obs>` block before the client sees it, then:

1. Stores the text in `sessions.summary` (SQLite)
2. Fires a goroutine to `POST /ingest` on the memory service (non-blocking)

The client response is never modified — stripping happens in the response stream.

### Embedding model

ChromaDB's default embedding function downloads `all-MiniLM-L6-v2` (~90 MB) on first use and caches it in the data volume. Subsequent starts are instant.

The collection uses cosine similarity (`hnsw:space: cosine`), which works well for sentence-length summaries.

## Services

| Path | Method | Description |
|------|--------|-------------|
| `/health` | GET | Returns `{"ok": true, "sessions": N}` |
| `/ingest` | POST | Upsert a session summary into ChromaDB |
| `/mcp/sse` | GET | MCP SSE endpoint (Claude Code connects here) |
| `/mcp/messages/` | POST | MCP message channel (used by the SSE client) |

### `POST /ingest`

```json
{
  "session_id": "uuid-string",
  "summary": "Fixed auth bug in proxy handler where JWT tokens were not validated.",
  "model": "claude-sonnet-4-6",
  "format": "anthropic",
  "token_count": 1500,
  "created_at_ms": 1716000000000
}
```

`upsert` semantics — safe to re-ingest if the session is reprocessed.

## MCP Tool

### `search_memories`

Search past sessions by semantic similarity to a free-text query.

**Parameters:**

| Name | Type | Default | Description |
|------|------|---------|-------------|
| `query` | string | required | Free-text search query |
| `n_results` | integer | 5 | Number of sessions to return |

**Returns:** JSON array of sessions, ordered by similarity:

```json
[
  {
    "session_id": "abc123",
    "summary": "Fixed auth bug in proxy handler where JWT tokens were not validated.",
    "model": "claude-sonnet-4-6",
    "format": "anthropic",
    "token_count": 1500,
    "created_at_ms": 1716000000000
  }
]
```

**Example usage in Claude Code:**

> "Search my memory for anything related to the compact logger compression work"

Claude calls `search_memories("compact logger zstd compression")` and uses the matching sessions as context.

## Configuration

| Environment Variable | Default | Description |
|----------------------|---------|-------------|
| `CHROMA_PATH` | `/data` | Path where ChromaDB persists its data |
| `PORT` | `8020` | HTTP port |

Data is persisted to `./chroma-data` on the host (bind-mounted into `/data`).

## Claude Code integration

The project `.mcp.json` pre-registers the server:

```json
{
  "mcpServers": {
    "lazy-memory": {
      "type": "sse",
      "url": "http://localhost:8020/mcp/sse"
    }
  }
}
```

On first use Claude Code will prompt you to approve the server. After that it's available in every session within this project directory.

The proxy's `MEMORY_INGEST_URL=http://memory:8020` environment variable enables ingestion. Remove it (or leave `MEMORY_INGEST_URL` unset) to disable — the proxy continues working normally without the memory service.

## Testing

```bash
# Run the integration test script (handles SSE + full MCP handshake)
uv run memory/test_mcp.py
```

The script:
1. Checks `/health`
2. Ingests 3 synthetic sessions
3. Opens an SSE connection and completes the MCP initialize handshake
4. Calls `tools/list` to verify the tool is advertised
5. Runs 3 semantic search queries and prints results

Expected output (after model download on first run):

```
==================================================
  Health
==================================================
  ✓ healthy — 0 sessions in ChromaDB

==================================================
  Ingest (test data)
==================================================
  (first ingest downloads the embedding model, may take a moment...)
  ✓ ingested test-001: Fixed authentication bug in the proxy handler...
  ✓ ingested test-002: Refactored the SQLite compact logger...
  ✓ ingested test-003: Added streaming SSE support to the proxy...

==================================================
  MCP SSE — connect
==================================================
  ✓ SSE connected — session URL: http://localhost:8020/mcp/messages/?session_id=...

==================================================
  MCP initialize
==================================================
  ✓ server: lazy-memory v?

==================================================
  tools/list
==================================================
  ✓ tool: search_memories — Search past Claude Code sessions semantically...

==================================================
  tools/call — search_memories
==================================================
  ✓ query "authentication security bug" → 2 result(s)
       session_id=test-001  summary=Fixed authentication bug...
  ...
```

## Stack

| Component | Technology |
|-----------|-----------|
| MCP protocol | `mcp[cli]` (official Anthropic Python SDK) |
| HTTP server | Starlette + Uvicorn |
| Vector store | ChromaDB (embedded, persistent) |
| Embedding model | `all-MiniLM-L6-v2` (via ChromaDB default) |
| Dependency management | `uv` + `pyproject.toml` |
| Container | Python 3.12 slim + uv |
