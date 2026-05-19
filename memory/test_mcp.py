#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = ["requests"]
# ///
"""Quick integration test for the lazy-memory MCP service."""

import json
import queue
import sys
import threading
import time
import requests

BASE = "http://localhost:8020"


def section(title):
    print(f"\n{'='*50}")
    print(f"  {title}")
    print(f"{'='*50}")


def ok(msg):
    print(f"  \033[32m✓\033[0m {msg}")


def fail(msg):
    print(f"  \033[31m✗\033[0m {msg}")
    sys.exit(1)


# ── 1. Health ────────────────────────────────────────────────────────────────

section("Health")
try:
    r = requests.get(f"{BASE}/health", timeout=5)
    r.raise_for_status()
    data = r.json()
    ok(f"healthy — {data['sessions']} sessions in ChromaDB")
except Exception as e:
    fail(f"health check failed: {e}")


# ── 2. Ingest test sessions ──────────────────────────────────────────────────

section("Ingest (test data)")
test_sessions = [
    {
        "session_id": "test-001",
        "summary": "Fixed authentication bug in the proxy handler where JWT tokens were not being validated correctly.",
        "model": "claude-sonnet-4-6",
        "format": "anthropic",
        "token_count": 1200,
    },
    {
        "session_id": "test-002",
        "summary": "Refactored the SQLite compact logger to use zstd compression for message bodies, reducing database size by 60%.",
        "model": "claude-sonnet-4-6",
        "format": "anthropic",
        "token_count": 3400,
    },
    {
        "session_id": "test-003",
        "summary": "Added streaming SSE support to the proxy, forwarding chunks to the client with flush-per-chunk.",
        "model": "claude-haiku-4-5-20251001",
        "format": "openai",
        "token_count": 800,
    },
]
for i, s in enumerate(test_sessions):
    # First ingest downloads the embedding model (~90MB), allow extra time
    timeout = 120 if i == 0 else 15
    if i == 0:
        print("  (first ingest downloads the embedding model, may take a moment...)")
    r = requests.post(f"{BASE}/ingest", json=s, timeout=timeout)
    if r.ok:
        ok(f"ingested {s['session_id']}: {s['summary'][:60]}...")
    else:
        fail(f"ingest failed for {s['session_id']}: {r.text}")


# ── 3. MCP SSE protocol ──────────────────────────────────────────────────────

section("MCP SSE — connect")

responses: queue.Queue = queue.Queue()
session_url: list[str | None] = [None]
sse_error: list[str | None] = [None]


def sse_reader():
    try:
        resp = requests.get(f"{BASE}/mcp/sse", stream=True, timeout=60)
        resp.raise_for_status()
        event_type = None
        for raw in resp.iter_lines():
            line = raw.decode() if isinstance(raw, bytes) else raw
            if line.startswith("event:"):
                event_type = line[6:].strip()
            elif line.startswith("data:"):
                data = line[5:].strip()
                if event_type == "endpoint":
                    path = data
                    session_url[0] = BASE + path if path.startswith("/") else path
                else:
                    try:
                        responses.put(json.loads(data))
                    except json.JSONDecodeError:
                        pass
                event_type = None
    except Exception as e:
        sse_error[0] = str(e)


t = threading.Thread(target=sse_reader, daemon=True)
t.start()

deadline = time.time() + 10
while session_url[0] is None and sse_error[0] is None:
    if time.time() > deadline:
        fail("timed out waiting for SSE endpoint event")
    time.sleep(0.05)

if sse_error[0]:
    fail(f"SSE error: {sse_error[0]}")

ok(f"SSE connected — session URL: {session_url[0]}")


def rpc(id_, method, params=None):
    payload = {"jsonrpc": "2.0", "id": id_, "method": method, "params": params or {}}
    r = requests.post(session_url[0], json=payload, timeout=5)
    if not r.ok:
        fail(f"POST {method} failed: {r.status_code} {r.text}")
    if id_ is None:
        return None
    try:
        return responses.get(timeout=15)
    except queue.Empty:
        fail(f"no response for {method} (id={id_})")


# ── 4. MCP initialize ────────────────────────────────────────────────────────

section("MCP initialize")
result = rpc(
    1,
    "initialize",
    {
        "protocolVersion": "2024-11-05",
        "capabilities": {},
        "clientInfo": {"name": "test-script", "version": "1.0"},
    },
)
if "result" in result:
    srv = result["result"].get("serverInfo", {})
    ok(f"server: {srv.get('name', '?')} v{srv.get('version', '?')}")
else:
    fail(f"initialize error: {result}")

# Send initialized notification (no response expected)
rpc(None, "notifications/initialized")
time.sleep(0.1)


# ── 5. tools/list ────────────────────────────────────────────────────────────

section("tools/list")
result = rpc(2, "tools/list")
if "result" in result:
    tools = result["result"].get("tools", [])
    for tool in tools:
        ok(f"tool: {tool['name']} — {tool.get('description', '')[:70]}")
    if not tools:
        fail("no tools returned")
else:
    fail(f"tools/list error: {result}")


# ── 6. tools/call — search_memories ─────────────────────────────────────────

queries = [
    "authentication security bug",
    "database compression optimization",
    "streaming proxy SSE",
]

section("tools/call — search_memories")
for i, query in enumerate(queries, start=3):
    result = rpc(i, "tools/call", {"name": "search_memories", "arguments": {"query": query, "n_results": 2}})
    if "result" in result:
        content = result["result"].get("content", [])
        if content:
            hits = json.loads(content[0]["text"])
            ok(f'query "{query}" → {len(hits)} result(s)')
            for hit in hits:
                print(f"       session_id={hit['session_id']}  summary={hit['summary'][:60]}...")
        else:
            ok(f'query "{query}" → empty (no matching sessions yet)')
    else:
        fail(f"search error: {result}")


section("Done")
print()
