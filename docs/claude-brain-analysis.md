# claude-brain: Architecture Analysis

## Project Overview

**claude-brain** is a local, single-user, SQLite-based persistent memory system for Claude Code. It captures every conversation exchange, indexes it, and provides multi-modal search (keyword, semantic, fuzzy). Runs entirely locally with zero cloud dependencies.

**Author:** Mike Dolan | **Stack:** Python (3.10+) | **License:** MIT

---

## Architecture

### Capture Layer (Hooks)

Six hooks fire at different Claude Code lifecycle points. All follow the rule: **stdout is sacred** (only valid JSON).

| Hook | When | What |
|------|------|------|
| `session-start.py` | Session opens | Loads context from last session notes, verifies pending items against brain DB, detects gaps |
| `user-prompt-submit.py` | Before each user message | Frustration detection, GO-check enforcement, keyword extraction + FTS5 search for memory injection |
| `stop.py` | After each Claude response | Calls `write_exchange.py` to capture new messages, triggers backup if >12h stale |
| `session-end.py` | Session exits | Auto-writes fallback session notes, auto-suggests tags, triggers backup |
| `pre-compact.py` | Before context compaction | Safety-net final capture via `write_exchange.py` |
| `post-compact.py` | After compaction | Re-injects brain context (project summary + last 5 session notes) |

### Capture Engine: `write_exchange.py`

Core live-capture script called after every response:

1. Opens the session's JSONL file
2. Iterates line by line, filtering `STORABLE_TYPES = {user, assistant, system}`; skips `{progress, file-history-snapshot, queue-operation}`
3. Deduplicates by message UUID (skips already-stored)
4. Detects project from path via `jsonl_project_mapping` config
5. Extracts text content (handles string and array-of-blocks; skips tool_use, tool_result, thinking, redacted_thinking, image blocks)
6. Inserts into `transcripts` table
7. Generates semantic embedding (if enabled, content >= 50 chars) → `transcript_embeddings`
8. Upserts `sys_sessions` with message count, timestamps, model info

### Batch Ingestion: `ingest_jsonl.py`

Bulk import from JSONL files (startup, imports from other platforms):

- Detects file type: `jsonl`, `subagent`, or `tool_result`
- Maps project via longest-match-first path matching
- Records in `sys_ingest_log` to prevent re-ingestion
- Also handles ChatGPT, Claude.ai, and Gemini data import scripts

### Storage: SQLite Database

**Connection:** WAL mode, 5s busy timeout, path from config.

**Schema:**

| Table | Purpose |
|-------|---------|
| `transcripts` | Every message from every session (id, session_id, project, uuid, parent_uuid, type, subtype, role, content, model, timestamp, tokens, stop_reason, is_subagent, source_file, raw_json, created_at, source) |
| `transcripts_fts` | FTS5 virtual table synced from transcripts(content) |
| `transcript_embeddings` | Semantic vectors (transcript_id FK, embedding BLOB 384 floats, model) |
| `sys_sessions` | Session metadata (session_id, project, started/ended_at, model, source, claude_version, cwd, message_count, notes, tags, quality_score) |
| `brain_facts` | Personal facts, cross-project (category, key, value, confidence) |
| `brain_preferences` | Personal preferences, cross-project (category, preference) |
| `facts` | Project-specific structured knowledge (project, category, key, value, source) |
| `decisions` | Locked decisions (decision_number PK, project, description, rationale) |
| `tool_results` | Tool call outputs (session_id, project, tool_use_id, content, source_file) |
| `sys_ingest_log` | Import dedup tracking (file_path, file_size, file_type, records_imported, ingested_at) |
| `project_registry` | Project name-to-prefix mapping (prefix PK, label, folder_name, summary, health, status) |

### Retrieval: Three Search Engines

All search is cross-project by default, with project-biased weighting.

#### 1. FTS5 Keyword Search
- Virtual table `transcripts_fts` synced from `transcripts`
- Supports operators: OR, AND, NOT, phrase matching
- **Recency-biased ranking:** `rank * (1.0 / (1.0 + julianday('now') - julianday(t.timestamp)))`
- Stop words filtered from extracted keywords

#### 2. Semantic Search (Vector)
- **Model:** `all-MiniLM-L6-v2` (~80MB, 384-dimensional float32 embeddings)
- Stored as BLOB (1536 bytes per embedding) in `transcript_embeddings`
- Cosine similarity computed in-memory via numpy
- Only messages with 50+ characters are embedded
- Optional: can be disabled in config

#### 3. Fuzzy Search (Typo Correction)
- Uses `FTS5VOCAB` introspection table for term frequencies
- Three correction rules:
  - Terms not in FTS at all → correct to best match (doc >= 10)
  - Rare terms (doc < 100) → correct only if close match has 20x+ frequency
  - Established terms (doc >= 100) → never corrected
- Uses `difflib.get_close_matches()` with 0.6 similarity cutoff, max 5 candidates

### Memory Injection at Prompt Time

The `user-prompt-submit.py` hook runs at every user prompt submission:

1. Extracts keywords from the prompt
2. Fuzzy-corrects keywords
3. Runs FTS5 search across all transcripts
4. **Project-biased search:** 3 results from current project + 7 global, merged and deduped
5. Returns top 5 matching memories as `additionalContext`
6. Also handles frustration detection (regex-based) and "GO" check enforcement

### MCP Server

11 read-only tools registered as `brain-server`:
`get_profile`, `get_project_state`, `search_transcripts`, `search_semantic`, `get_session`, `get_recent_sessions`, `lookup_decision`, `lookup_fact`, `get_recent_summaries`, `get_status`, `get_schema`

### Additional Features

- **Email digests:** Daily standup, weekly digest, project deep-dive via Gmail SMTP
- **Brain health:** 9-point diagnostic script
- **Tagging:** Auto-suggested tags from transcript content, batch tag review via spreadsheet
- **Backup:** Rotating DB backup (`brain_sync.py`), 12-hour interval trigger
- **Config:** YAML-based (`config.yaml`), gitignored

### Design Summary

| Aspect | Approach |
|--------|----------|
| Capture | JSONL file scanning via Claude Code hooks (session lifecycle) |
| Dedup | Message UUID-based |
| Storage | Single SQLite DB (WAL mode) |
| Indexing | FTS5 (keyword) + MiniLM embeddings (semantic) |
| Search | 3-engine: FTS5 keyword + cosine similarity + fuzzy typo correction |
| Retrieval | Project-biased FTS5 search injected at prompt time + MCP tools |
| Embedding | `all-MiniLM-L6-v2`, 384-dim, only for 50+ char messages |
| Scope | Single-user, local-only, no cloud |
