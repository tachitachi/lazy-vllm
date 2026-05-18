# Memory Distillation: Rule-Based Filtering + `<memory>` Tag Injection

## Background

lazy-vllm's proxy captures every message at the wire level via `CompactLogger`. The plan is to
feed those messages into a vector store (ChromaDB or sqlite-vec) so that a `UserPromptSubmit`
hook can retrieve semantically similar past interactions and inject short blurbs into the prompt.

The core problem: raw proxy messages are noisy. A single Claude Code turn produces 10+ proxy
calls (tool reads, edits, bash commands, observations), each with its own session. The stored
content is dominated by:

- Large system prompts repeated verbatim every request
- Tool results containing full file contents (often thousands of tokens)
- Thinking/reasoning blocks that are internal to one response
- Repeated context window padding (the same conversation replayed across turns)

Embedding all of this verbatim produces a vector store full of near-identical documents that are
expensive to store and retrieve poorly. Distillation solves this before anything goes into the
vector store.

---

## The Two-Option Strategy

The approach combines two complementary techniques, both zero-LLM-cost or near-zero:

| | Option 1 | Option 2 |
|---|---|---|
| **Mechanism** | Rule-based filter: keep user+assistant text, drop everything else | Inject `<memory>` annotation request into system prompt; strip and store the block |
| **Extra inference cost** | None | ~50–100 tokens per qualifying response |
| **Quality** | Moderate — user intent is clean, but misses buried decisions | High — Claude's own summary of what it just did |
| **When it fires** | Every turn | Turns where assistant text response is ≥ N chars |
| **Output** | Filtered (user message, assistant text) pair | 2–3 sentence observation written by Claude |

They are not alternatives — they run together. Option 2 output becomes the **primary** ChromaDB
document when available. Option 1 output is the **fallback** and supplemental record.

---

## Option 1: Rule-Based Filtering

### What to Keep vs. Drop

Based on the `Message` and `OutputLog` types in `proxy/internal/logger/parse.go`:

| Content Type | Action | Reason |
|---|---|---|
| `role=user`, text content | **Keep (full)** | Direct intent signal — best embedding target |
| `role=assistant`, `Content` field | **Keep (full)** | What was done / responded |
| `role=assistant`, `ReasoningContent` field | **Drop** | Internal monologue, high noise for retrieval |
| `role=system` | **Drop** | Boilerplate, repeated every request, not useful for retrieval |
| `role=tool` (tool results) | **Truncate to 300 chars** | Tool outputs are large but their start is usually the meaningful part |
| `OutputLog.ToolCalls` | **Keep tool names only** | `Read`, `Edit`, `Bash` — action log without argument noise |

### Chunking Strategy

Each ChromaDB document is one **user turn pair**: the user message plus the assistant's text
response. This gives documents of 50–500 tokens, which embed cleanly.

```
Document: "user: <user message text>\nassistant: <assistant content text>"
Metadata: {session_id, timestamp, model, tool_names[], format}
```

Do not chunk mid-assistant-response or include tool exchange internals in the same document as the
user message. The tool calls are metadata, not part of the embedded text.

### Filter Logic (Pseudocode)

```go
func FilterForMemory(msgs []logger.Message, output logger.OutputLog) (string, []string) {
    var userText string
    var toolNames []string

    for _, msg := range msgs {
        switch msg.Role {
        case "user":
            if text := extractText(msg.Content); len(text) > 50 {
                userText = text  // last user message wins
            }
        case "tool":
            // skip tool results entirely from the embedding text
        }
    }

    // Extract tool names from output (not arguments)
    for _, tc := range output.ToolCalls {
        toolNames = append(toolNames, tc.Function.Name)
    }

    assistantText := output.Content  // already a clean string from OutputLog
    if userText == "" || assistantText == "" {
        return "", toolNames
    }
    return "user: " + userText + "\nassistant: " + assistantText, toolNames
}
```

This function already has all the data it needs from the existing parse path — nothing new to
extract from the wire.

---

## Option 2: `<memory>` Tag Injection

### How It Works

The proxy intercepts the request before forwarding, appends a short instruction to the system
prompt, and intercepts the response after capture to extract and remove the `<memory>` block
before it reaches the client.

```
Client → Proxy (inject system prompt addition) → Anthropic/Claude
Claude → Proxy (capture response, strip <memory> block) → Client
                      ↓
             Store <memory> content in ChromaDB
```

Claude Code never sees the `<memory>` tag. It's stripped at the proxy layer.

### System Prompt Injection

The addition to append to any existing system prompt text:

```
After your response, write a <memory> block (do not show it in your answer):
<memory>
[1-3 sentences: what the user asked for, what you did or built, any key decisions or files changed]
</memory>
Keep it factual and brief. Only write one <memory> block.
```

This follows the same pattern as `injectDisableThinking` in
`proxy/internal/proxy/server.go` — mutate the request body before forwarding.

For **Anthropic format**, the system field can be a string or a `[]anthropicBlock`. Both cases
need to be handled: append to the string, or add a new `text` block.

For **OpenAI format**, `messages[0].role == "system"` — append to its `content` string.

### Response Stripping

The `responseCapture` in `proxy/internal/proxy/proxy.go` already buffers the full response. After
`forwardRequest` returns, the captured bytes are already in `rc.buf`. The stripping step:

1. Scan the buffered response for `<memory>...</memory>`
2. Extract the inner text → this is the observation to store
3. Remove the `<memory>...</memory>` block from the buffered bytes
4. Replace `rc.buf` with the cleaned bytes before it is read by the output parser

For **streaming responses**: the `<memory>` block will appear as SSE chunks near the end of the
stream. The existing `flushCopy` writes bytes as they arrive — the client would briefly see the
`<memory>` tag before it could be stripped. Two options:

- **Simple: disable streaming for Option 2** — set `stream: false` on requests where memory injection is active. Adds latency. Acceptable for a V1.
- **Better: buffer-then-stream** — buffer the full response, strip `<memory>`, then write buffered bytes to client as if it were streaming. This is more complex but transparent.

`responseCapture` already dual-writes to `rc.buf` and `w`. The fix is to buffer-only during the
capture phase and write to `w` after stripping.

### When to Apply Option 2

Skip injection for low-signal turns to avoid wasting tokens:

- Assistant `Content` would be < 200 characters (short answers, tool-only responses)
- Request is a token-counting call (`/v1/messages/count_tokens`)
- Model is a `-FLASH` variant (likely a background/cheap call, not a substantive turn)

The content length can't be known before the response arrives, so the injection happens
unconditionally but the extracted `<memory>` block is discarded if the response content is short.

---

## Combined Strategy

```
Every qualifying proxy call
    │
    ├── Option 2: inject <memory> instruction into system prompt
    │
    ├── Forward to upstream, capture response
    │
    ├── Extract <memory> block from response
    │   ├── Found (assistant wrote a substantive observation)?
    │   │   → Use as primary ChromaDB document
    │   │   → Strip from response bytes before returning to client
    │   └── Not found (short turn, tool-only, etc.)?
    │       → Fall through to Option 1
    │
    ├── Option 1: rule-based filter on (msgs, output)
    │   → "user: ... \nassistant: ..." pair as ChromaDB document (if ≥ 50 chars)
    │
    └── Write to ChromaDB async (non-blocking)
        Metadata: {session_id, timestamp, model, format, tool_names, source: "memory_tag"|"filtered"}
```

The `source` metadata field lets you filter in retrieval — if you only want high-signal memories,
filter on `source=memory_tag`. The filtered pairs are still useful for broad semantic recall.

---

## Infrastructure Requirements

### Embedding Service

The proxy is Go. The embedding model needs to be callable from Go. Options:

**Recommended: Ollama sidecar (CPU-only)**

Add to `docker-compose.yml`:
```yaml
ollama:
  image: ollama/ollama
  volumes:
    - ollama-models:/root/.ollama
  # No GPU reservations — runs on CPU deliberately, doesn't compete with Qwen/Gemma
  environment:
    - OLLAMA_KEEP_ALIVE=24h
```

Use `nomic-embed-text` (~275MB, 768-dim) or `all-minilm` (~45MB, 384-dim). Both available via
`ollama pull`. The proxy calls `POST http://ollama:11434/api/embeddings` — same OpenAI-style REST
API it already uses for upstream calls.

Embedding latency on CPU: ~5–20ms per document for `all-minilm`, ~20–80ms for `nomic-embed-text`.
Since writes are async, this doesn't block the response path.

**Alternative: vLLM embedding endpoint**

vLLM supports `GET /v1/embeddings`. If Qwen3.6 is running locally, it can serve embeddings too.
The downside: competes with the main model for GPU KV cache and batch queue. Not recommended for
background memory writes.

### Vector Store

**Recommended: ChromaDB (Docker service)**

The session-exclusion strategy for ChromaDB is already fully designed in
`docs/chromadb-session-exclusion-analysis.md`. Add to `docker-compose.yml`:

```yaml
chromadb:
  image: chromadb/chroma:latest
  ports:
    - "8005:8000"
  volumes:
    - chromadb-data:/chroma/.chroma/index
```

The proxy calls ChromaDB's REST API directly from Go — no Python subprocess, no subprocess
lifecycle management (the complexity nightmare documented in
`docs/chromadb-vs-claude-brain-analysis.md` applies to the MCP-based approach; direct HTTP calls
avoid it entirely).

**Alternative: sqlite-vec**

`github.com/asg017/sqlite-vec` provides embedded HNSW in SQLite. The catch: `modernc.org/sqlite`
(the pure-Go driver used by the proxy) does not support loadable extensions, which is how
`sqlite-vec` is distributed. Using sqlite-vec would require switching to `mattn/go-sqlite3` (CGO)
or running sqlite-vec in a separate process. ChromaDB as a Docker service is lower friction.

### New Services Summary

| Service | Image | Purpose | CPU/GPU |
|---|---|---|---|
| `chromadb` | `chromadb/chroma:latest` | Vector store | CPU |
| `ollama` | `ollama/ollama` | Embedding model | CPU |

Both are stateless enough to restart without data loss (chromadb has a volume, ollama model is
pulled once on first use).

---

## Proxy Code Changes

### 1. `proxy/internal/proxy/server.go` — system prompt injection

New function mirroring `injectDisableThinking`:

```go
func injectMemoryInstruction(body []byte, format string) []byte
```

For OpenAI format: find `messages[0]` where `role == "system"` and append the instruction. If no
system message exists, prepend one.

For Anthropic format: unmarshal the `system` field (string or `[]block`), append to it.

### 2. `proxy/internal/proxy/proxy.go` — buffered response capture

Current `responseCapture` dual-writes to `rc.buf` and `w` simultaneously. For memory extraction,
this needs to change to: write to `rc.buf` only, then strip `<memory>`, then write cleaned bytes
to `w`. This changes the streaming behavior — callers of `forwardRequest` need to opt in.

New variant: `forwardWithCapture` that returns the raw bytes instead of streaming them. Used by
`forwardWithLogging` when memory is enabled.

### 3. `proxy/internal/proxy/handlers.go` — wire it all together

In `forwardWithLogging`, after the existing output logging:

```go
// Memory distillation (async, non-blocking)
if memoryLogger != nil {
    go memoryLogger.Store(sessionID, msgs, output, rc.buf.Bytes(), format)
}
```

`MemoryLogger.Store` runs in a goroutine: extract `<memory>` tag or fall back to filtered pair,
embed via ollama, write to ChromaDB with metadata.

### 4. `proxy/internal/logger/memory.go` — new file

`MemoryLogger` struct encapsulating:
- `FilterForMemory(msgs, output) (text, toolNames)` — Option 1 logic
- `ExtractMemoryTag(responseBytes) (tag, cleanedBytes)` — Option 2 extraction
- `Embed(text) ([]float32, error)` — call ollama embeddings endpoint
- `Store(id, text, metadata)` — write to ChromaDB

---

## Hook Changes

### `UserPromptSubmit` hook (new)

The hook reads the user's prompt and queries ChromaDB for the top-N most similar past documents,
then injects them as `additionalContext`. Calling convention: hook returns JSON with
`additionalContext` field.

The hook needs:
1. The current project path (`cwd` is available in hook context)
2. A ChromaDB query endpoint on the memory service
3. The current session's recent session IDs (for exclusion — see session exclusion doc)

Simple approach for V1: the hook calls `GET http://localhost:8005/api/v1/collections/memories/query`
directly. The ChromaDB HTTP API is straightforward.

---

## What This Defers

These are explicitly out of scope for this approach and left for later phases:

- **Session boundary detection**: The proxy still creates a new session per HTTP request. Memory documents are turn-level, not conversation-level. Session-end summaries require a `Stop`/`SessionEnd` hook + a separate summarization pass.
- **`files_modified` metadata**: Knowing which files were touched per interaction requires a `PostToolUse` hook that the proxy can't provide. High value for queries like "what changed in proxy.go last week" — a Phase 2 addition.
- **MCP server for deep recall**: The full-context retrieval tool (point 3 of the overall plan) is independent of this distillation work and can be built once the ChromaDB collection is populated.
- **Async distillation queue**: The `go memoryLogger.Store(...)` goroutine approach is fine for V1 but doesn't survive proxy restarts. A persistent queue (simple SQLite table as a job queue) would make it durable.
- **Recency weighting**: ChromaDB returns results sorted by cosine distance. A recency bias (e.g., multiply distance by `1 / (1 + days_since)`) would improve results but requires post-query reranking that isn't in scope here.
