# claude-mem: Architecture Analysis

## Project Overview

**claude-mem** is a persistent memory system for Claude Code (and other AI coding tools: Gemini CLI, Cursor, OpenCode, Windsurf, Codex). It captures tool-use observations, generates semantic summaries, and injects relevant context into future sessions for cross-session continuity.

**Stack:** TypeScript/Node.js (Bun runtime) | **Version:** 13.2.0

---

## Architecture

### Capture Layer (Hooks + File Watching)

Three parallel capture mechanisms feed the system:

#### 1. PostToolUse Hook
- Fires after every tool call (Read, Edit, Write, Bash, etc.)
- Sends tool name, input, and output to the worker via `ingestObservation()`
- `TranscriptEventProcessor` processes transcript lines

#### 2. Transcript File Watching (`TranscriptWatcher`)
- Uses `fs.watch` to tail transcript JSONL files in real-time
- `FileTailer` reads new lines from files, tracking offset via state file
- `TranscriptEventProcessor` handles events: `session_init`, `user_message`, `assistant_message`, `tool_use`, `tool_result`, `observation`, `file_edit`, `session_end`
- Supports multiple IDE transcript formats (Claude Code, Codex, Gemini CLI, Cursor, Windsurf) via schema field-mapping

#### 3. SessionEnd Hook
- Queues a summary request via `POST /api/sessions/summarize`
- Triggers context update: fetches injected context, writes to `AGENTS.md`

### Storage: SQLite Database

**Path:** `~/.claude-mem/claude-mem.db` (configurable via `CLAUDE_MEM_DATA_DIR`)

**PRAGMAs:** WAL mode, synchronous=NORMAL, foreign_keys=ON, temp_store=memory, mmap_size=256MB, cache_size=10000

**Schema:**

| Table | Purpose | Key Fields |
|-------|---------|-----------|
| `sdk_sessions` | Session tracking | id PK, content_session_id UNIQUE, memory_session_id UNIQUE, project, platform_source, user_prompt, custom_title, started/completed_at (+epoch), status |
| `observations` | Tool-use observations (what agent did/learned) | id PK, memory_session_id, project, text, type (decision/bugfix/feature/refactor/discovery/change), title, subtitle, facts (JSON), narrative, concepts (JSON), files_read/files_modified (JSON), prompt_number, discovery_tokens, agent_type/agent_id, content_hash UNIQUE, generated_by_model, relevance_count, metadata |
| `session_summaries` | AI-generated session summaries | id PK, memory_session_id, project, request, investigated, learned, completed, next_steps, files_read/edited (JSON), notes, prompt_number, discovery_tokens |
| `user_prompts` | User prompt text per session | id PK, content_session_id, prompt_number, prompt_text |
| `pending_messages` | Async observation processing queue | id PK, session_db_id FK, content_session_id, message_type (observation/summarize), tool_name/input/response, cwd, last_user/assistant_message, prompt_number, status |

**FTS5 Virtual Tables:**
- `observations_fts`: (title, subtitle, narrative, text, facts, concepts) with auto-sync triggers (ai/ad/au)
- `session_summaries_fts`: (request, investigated, learned, completed, next_steps, notes)
- `user_prompts_fts`: (prompt_text)

**Deduplication:** SHA256 hash of `(memory_session_id + title + narrative).slice(0,16)` stored as `content_hash`; enforced via `ON CONFLICT DO NOTHING`

**Migrations:** 32+ migration versions tracked in `schema_versions` table, applied sequentially via `DatabaseManager`

### Worker Service Architecture

**`WorkerService`** — Express server on port `37700 + (uid % 100)`:

Startup initializes:
1. Mode configuration
2. One-time migrations (Chroma, CWD remap, worktree adoption)
3. `ChromaMcpManager` (if enabled)
4. `DatabaseManager` (database + migrations)
5. `SearchManager` with `ChromaSync`
6. All search routes
7. `TranscriptWatcher` (file watching)
8. Chroma backfill for all projects

### Retrieval: Three-Tier Search

**`SearchManager`** orchestrates a three-tier search strategy:

#### 1. Chroma Semantic Search (Primary)
- Primary path for queries with text
- `ChromaSync` manages embedding sync

#### 2. SQLite FTS5 Keyword Search (Fallback)
- Fallback when Chroma is unavailable
- Uses `observations_fts`, `session_summaries_fts`, `user_prompts_fts`

#### 3. SQLite Metadata Filtering
- Filter-only queries (no text query)
- By type, concept, file, project, date range

#### Chroma Vector Search Details (`ChromaSync`)

**Document Format:** Each observation spawns multiple Chroma documents for granularity:
- `obs_{id}_narrative` — narrative field
- `obs_{id}_text` — raw text field
- `obs_{id}_fact_{i}` — each fact as a separate document

**Metadata fields:** `sqlite_id`, `doc_type`, `memory_session_id`, `project`, `merged_into_project`, `created_at_epoch`, `type`, `title`, `subtitle`, `concepts`, `files_read`, `files_modified`, `field_type`

**Backfill Pipeline (`backfillAllProjects`):**
- Runs on worker startup
- Projects processed in chunks of 3 for concurrency
- Per-project watermarks (highest synced observation/summary/prompt ID)
- Non-contiguous failure guard: if any batch fails, subsequent batches skip watermark bump
- De-duplicates Chroma results by `sqlite_id`

**Query Flow:** `queryChroma(query, limit, whereFilter)` → deduplicate by `sqlite_id` → hydrate full records from SQLite

### Context Injection Pipeline

**`ContextBuilder.generateContext()`** — Assembles context for injection into Claude Code prompts:

1. Receives `ContextInput` with project(s), session ID, cwd
2. `queryObservations()` — Fetches recent observations filtered by project + type + concepts
3. `querySummaries()` — Fetches recent session summaries
4. `buildContextOutput()` — Assembles:
   - Header (project info, token economics)
   - Timeline (chronological mix of observations + summaries)
   - Summary fields (most recent summary)
   - Previously section (prior session messages)
   - Footer (token savings, version)

**Injection chain:**
- `POST_TOOL_USE` or `USER_PROMPT_SUBMIT` hooks → `GET /api/context/inject?projects=...`
- Triggers `generateContext()` → returns context text → injected into Claude Code prompt

### Search API (16 endpoints)

| Endpoint | Purpose |
|----------|---------|
| `GET /api/search` | Unified search |
| `GET /api/timeline` | Timeline around anchor |
| `GET /api/decisions` | Decision observations |
| `GET /api/changes` | Change-related observations |
| `GET /api/how-it-works` | "How it works" observations |
| `GET /api/search/observations` | Search observations |
| `GET /api/search/sessions` | Search session summaries |
| `GET /api/search/prompts` | Search user prompts |
| `GET /api/search/by-concept` | Find by concept tag |
| `GET /api/search/by-file` | Find by file path |
| `GET /api/search/by-type` | Find by type |
| `GET /api/context/recent` | Recent session context |
| `GET /api/context/timeline` | Timeline around anchor |
| `GET /api/context/preview` | Preview context |
| `GET /api/context/inject` | Inject context for hooks |
| `POST /api/context/semantic` | Semantic context query |

### Additional Features

- **Multi-IDE support:** `platform_source` tracks IDE (claude, codex, cursor, gemini-cli, opencode, windsurf, raw, openclaw)
- **Privacy:** `<private>` tags stripped at hook layer before data reaches database
- **Multi-account:** `CLAUDE_MEM_DATA_DIR` env var for profile isolation
- **SSE Broadcasting:** Real-time event streaming to web viewer
- **Session Queue:** Optional Redis-backed BullMQ for high-throughput observation processing
- **Worktree support:** Auto-adopts observations from merged worktree branches
- **Knowledge Agent:** Knowledge graph/corpus building
- **PostgreSQL storage:** Optional cloud storage for multi-user scenarios
- **Web Viewer:** React SPA showing real-time activity feed
- **MCP Server:** External integration tools
- **OpenClaw:** Telegram/Discord/Slack integration gateway

### Design Summary

| Aspect | Approach |
|--------|----------|
| Capture | Hooks (PostToolUse, SessionEnd) + file watcher (fs.watch on JSONL transcripts) |
| Granularity | Observation-level (per tool call) + session-level summaries |
| Dedup | SHA256 hash of (session_id + title + narrative) |
| Storage | SQLite (WAL, 32+ migrations, FTS5 auto-sync triggers) |
| Indexing | FTS5 (keyword) + ChromaDB (semantic vectors) |
| Search | 3-tier: Chroma semantic → FTS5 fallback → metadata filter |
| Retrieval | ContextBuilder pipeline injected at hook time (POST_TOOL_USE, USER_PROMPT_SUBMIT) |
| Embedding | ChromaDB (model unspecified in code; multi-granularity doc splitting: narrative, text, facts) |
| Scope | Multi-IDE, multi-user (PostgreSQL optional), cloud-ready |
| Type System | Rich observation taxonomy: decision, bugfix, feature, refactor, discovery, change |
