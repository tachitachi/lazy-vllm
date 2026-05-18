# ChromaDB vs. claude-brain: Vector Search Comparison

## The Core Difference

**claude-brain** computes semantic search by loading ALL embeddings into RAM and doing a numpy dot product over every single row. **ChromaDB** (used by claude-mem) is a purpose-built vector store with indexed ANN (approximate nearest neighbor) search.

---

## Query Architecture

### claude-brain: In-Memory Brute Force

```python
# search_semantic() in brain_search.py
rows = conn.execute("""
    SELECT e.transcript_id, e.embedding, t.session_id, t.project,
           t.content, t.timestamp
    FROM transcript_embeddings e
    JOIN transcripts t ON t.id = e.transcript_id
    WHERE t.content IS NOT NULL AND length(t.content) > 30
""").fetchall()

results = []
for row in rows:
    emb = np.frombuffer(row[1], dtype=np.float32)
    sim = float(np.dot(query_emb, emb))
    results.append((sim, row))

results.sort(key=lambda x: x[0], reverse=True)
return results[:limit]
```

- **Algorithm:** Exhaustive brute-force — `np.dot(query, every_embedding)` in a Python for-loop
- **Scaling:** O(N) — fetches all rows from SQLite, computes N dot products
- **Data fetch:** `SELECT * FROM transcript_embeddings JOIN transcripts` — every row
- **Bottleneck:** Memory + CPU linear scan grows proportionally with corpus size
- **Model load:** `SentenceTransformer("all-MiniLM-L6-v2")` loads on every query invocation (cold start penalty of ~500ms–2s depending on disk)

### ChromaDB: ANN Vector Index

```typescript
// queryChroma() in ChromaSync.ts
results = await chromaMcp.callTool('chroma_query_documents', {
  collection_name: this.collectionName,
  query_texts: [query],
  n_results: limit,
  where: whereFilter,
  include: ['documents', 'metadatas', 'distances']
});
```

- **Algorithm:** ANN index (HNSW or similar) — sub-linear tree traversal
- **Scaling:** ~O(log N) — returns only top-k results from the index
- **Data fetch:** Chroma returns only the matched IDs, distances, and metadata
- **Bottleneck:** Index build cost at insert time, fast query
- **Model load:** Chroma's Python subprocess stays warm; no cold start per query

### Performance by Corpus Size

| Corpus Size | claude-brain (est.) | ChromaDB (est.) |
|-------------|-------------------|-----------------|
| 1,000 messages | ~50ms (trivial) | ~10ms |
| 10,000 messages | ~500ms (loading + scan) | ~15ms |
| 50,000 messages | ~2.5s (problematic) | ~20ms |
| 100,000 messages | ~5s+ (UX-breaking) | ~25ms |
| 500,000 messages | ~25s (impractical) | ~30ms |

At 1,000 transcripts the claude-brain approach is fine. At 100,000 it's loading ~150MB of float32 data into memory and running 100K dot products in a Python loop. Chroma handles millions via an index.

---

## Storage Architecture

### Embedding Storage

| Aspect | claude-brain | ChromaDB (claude-mem) |
|--------|-------------|----------------------|
| **Where vectors live** | SQLite BLOB column (`transcript_embeddings.embedding`) | Chroma's own storage (`~/.claude-mem/chroma/`) |
| **Per-source granularity** | 1 embedding per transcript row (1:1) | Multiple docs per observation: narrative, text, each fact separately |
| **Embedding model** | `all-MiniLM-L6-v2` (384-dim, local via sentence-transformers) | Same model via `chroma-mcp` (spawns Python subprocess via `uvx`) |
| **Storage size per message** | 384 × 4 bytes = 1,536 bytes | 384 × 4 bytes + index overhead per doc × N docs per observation |

### Document Splitting

**claude-brain:** One embedding captures the entire message. If a 2,000-word assistant response contains a 10-word fact at line 150, that fact is semantically diluted in the embedding.

**ChromaDB:** Each observation spawns multiple documents for fine-grained retrieval:

```typescript
// formatObservationDocs() in ChromaSync.ts
documents.push({ id: `obs_${id}_narrative`, document: narrative });
documents.push({ id: `obs_${id}_text`, document: text });
facts.forEach((fact, i) => {
  documents.push({ id: `obs_${id}_fact_${i}`, document: fact });
});
```

Similarly, session summaries split into: `request`, `investigated`, `learned`, `completed`, `next_steps`, `notes`. This means a query about "database migration strategy" can match against a single fact like "Chose Postgres over MySQL for JSON support" rather than an entire observation about refactoring auth.

### Metadata

| Aspect | claude-brain | ChromaDB (claude-mem) |
|--------|-------------|----------------------|
| **Metadata** | None — just `transcript_id` FK | `sqlite_id`, `doc_type`, `project`, `type`, `title`, `subtitle`, `concepts`, `files_read`, `files_modified`, `created_at_epoch`, `field_type`, `merged_into_project`, `prompt_number` |
| **Filtering** | Project filter as SQLite WHERE before scan | Chroma metadata filter (`where: { project: "x", type: "decision" }`) |
| **Result dedup** | Not needed (1 doc per message) | By `sqlite_id` — multiple docs from the same observation collapse to one result |

---

## Sync & Backfill

### Embedding Creation

| Aspect | claude-brain | ChromaDB (claude-mem) |
|--------|-------------|----------------------|
| **At capture time** | `write_exchange.py` embeds each new message (if enabled, ≥50 chars) | `ChromaSync.syncObservation()` embeds at observation storage time |
| **Backfill** | `batch_embed.py`: LEFT JOIN finds un-embedded rows, batch of 500 | `backfillAllProjects()`: watermark-based, per-project, concurrent (3 projects) |
| **Incremental tracking** | LEFT JOIN on `transcript_embeddings.transcript_id IS NULL` | Per-project watermarks (highest synced observation/summary/prompt ID) |
| **Failure recovery** | Safe to re-run (`INSERT OR REPLACE`) | Watermark doesn't advance on partial batch; non-contiguous failure guard; next boot re-attempts |

### Backfill Complexity

**claude-brain** (`batch_embed.py`): ~60 lines. Find unembedded, loop, embed, insert. Done.

**ChromaDB** (`ChromaSync`): ~450 lines. The complexity comes from:
- Non-contiguous failure guard: once any batch under-writes, ALL subsequent batches skip the watermark bump (because a single monotonic ID can't represent "synced through 200, gap at 201–250, then 251 onward")
- Reconcile-on-duplicate: Chroma `add` fails on existing IDs, so delete-then-re-add
- Per-document watermark tracking (observations, summaries, prompts have separate watermarks)
- Bootstrap from Chroma: if local watermark file is missing, scan Chroma for existing IDs to rebuild watermarks
- Subprocess lifecycle: the Chroma subprocess itself needs connection management, timeout handling, reconnect backoff, and process tree cleanup

---

## Operational Complexity

### claude-brain

```
No moving parts. Pure Python scripts that read/write SQLite.
No external processes, no network calls, no subprocess management.
```

Dependencies: `sentence-transformers`, `numpy` (optional — can disable semantic search entirely)

### ChromaDB (claude-mem)

```
MCP subprocess chain: uvx → uv → python → chroma-mcp → chromadb
```

The `ChromaMcpManager` class is 700+ lines of code. ~200 of those lines are just process tree cleanup (`killProcessTree`, `collectDescendantPids`) to handle the fact that on Linux, when the MCP SDK closes a transport, the grandchildren (uv/python/chroma-mcp) get re-parented to init and survive as orphans. The manager handles:

- Singleton enforcement (at most one chroma-mcp subprocess per worker)
- Connection lifecycle with 30s timeout and 10s reconnect backoff
- Transport error recovery with automatic retry
- Process tree kill on Linux (bottom-up SIGTERM → SIGKILL cascade)
- Process tree kill on Windows (`taskkill /T /F`)
- SSL certificate bundling for enterprise proxies (Zscaler)
- Telemetry suppression (`ANONYMIZED_TELEMETRY=false`)
- Remote Chroma mode (HTTP client connecting to remote Chroma instance)

Plus the `ChromaSync` class adds another ~500 lines for backfill pipeline, watermark management, document formatting, and query deduplication.

---

## Query Flow Comparison

### claude-brain Search Path

```
User query
  → extract_keywords() [stop word removal, MAX_KEYWORDS=10]
  → fuzzy_correct() [FTS5VOCAB typo correction]
  → FTS5 search [OR query across keywords, recency-biased ranking]
  → semantic search [optional, loads model, fetches all embeddings, O(N) scan]
  → format results separately (keyword matches, semantic matches)
```

**Three search engines:** FTS5 keyword + semantic vector + fuzzy typo correction. Combined results are shown as separate sections.

### ChromaDB Search Path (claude-mem)

```typescript
// SearchManager.search()
if (!query) {
  // PATH 1: Filter-only (metadata, no text)
  results = sessionSearch.searchObservations(undefined, options);
} else if (chromaSync) {
  // PATH 2: Chroma semantic search (primary)
  chromaResults = queryChroma(query, 100, whereFilter);
  ids → hydrate full records from SQLite
} catch {
  // PATH 3: FTS5 fallback (Chroma unavailable)
  results = sessionSearch.searchObservations(query, options);
}
```

**Three-tier fallback:** Chroma semantic → FTS5 keyword → metadata filter. Only one result set (Chroma is primary, FTS5 is fallback, not combined).

---

## Key Trade-offs Summary

### Choose claude-brain (in-memory) when:
- Corpus is small (< 20,000 messages)
- You value simplicity over scalability
- You want zero external dependencies (no subprocesses, no network)
- You want fuzzy typo correction alongside semantic search
- You want combined keyword + semantic results (both engines run, results shown together)

### Choose ChromaDB when:
- Corpus is large or growing (10,000+ messages)
- You need fine-grained retrieval (per-fact, per-field semantic matching)
- You need rich metadata filtering (by type, project, concept, file, date)
- You want sub-20ms query latency at scale
- You can tolerate the operational overhead (subprocess management, remote mode for multi-user)

### What ChromaDB adds that claude-brain lacks:
1. **Fine-grained retrieval** — individual facts are searchable, not whole messages
2. **Metadata filtering** — scope queries by type, project, concept, file at search time
3. **Scalable latency** — O(log N) vs O(N)
4. **Remote mode** — multiple users can share a Chroma instance
5. **Graceful degradation** — FTS5 fallback when Chroma is unavailable

### What claude-brain does that ChromaDB doesn't:
1. **Fuzzy typo correction** — FTS5VOCAB-based correction before search
2. **Recency-biased ranking** — FTS5 results weighted by `1 / (1 + days_since)`
3. **Combined results** — keyword AND semantic results shown together, not as fallback
4. **Simplicity** — no subprocess, no IPC, no network, no operational surface

---

## The Missing Middle

Neither approach is perfect. The ideal system would combine:

1. **ANN index** (ChromaDB-style) for scalable query latency
2. **Fine-grained document splitting** (ChromaDB-style) for per-fact matching
3. **Fuzzy correction + recency bias** (claude-brain-style) for keyword search quality
4. **Combined results** (claude-brain-style) — run both FTS5 and vector, merge, not fallback
5. **Low operational overhead** — embedded ANN (e.g., SQLite with vector extension via FTS6 + `vector0` or sqlite-vec) to avoid the subprocess chain

The `sqlite-vec` project (https://github.com/asg017/sqlite-vec) offers embedded ANN search directly in SQLite with HNSW indexing — it would give ChromaDB-like query performance and fine-grained splitting without any external subprocess, bridging the gap between these two approaches.
