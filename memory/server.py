import json
import logging
import os

import chromadb
import uvicorn
import mcp.types as types
from mcp.server import Server
from mcp.server.sse import SseServerTransport
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

CHROMA_PATH = os.getenv("CHROMA_PATH", "/data")
PORT = int(os.getenv("PORT", "8020"))
DEFAULT_THRESHOLD = float(os.getenv("MEMORY_THRESHOLD", "0.5"))

_log_level = getattr(logging, os.getenv("LOG_LEVEL", "INFO").upper(), logging.INFO)
logging.basicConfig(level=_log_level, format="%(levelname)s  %(message)s")
log = logging.getLogger("memory")

chroma = chromadb.PersistentClient(path=CHROMA_PATH)
collection = chroma.get_or_create_collection(
    "sessions",
    metadata={"hnsw:space": "cosine"},
)


def parse_obs_field(text: str, field: str) -> str:
    for line in text.splitlines():
        if line.startswith(f"{field}:"):
            return line[len(field) + 1:].strip()
    return ""


def format_memory(doc: str, metadata: dict) -> str:
    obs_type  = parse_obs_field(doc, "type") or metadata.get("obs_type", "")
    topic     = parse_obs_field(doc, "topic")
    claim     = parse_obs_field(doc, "claim") or doc
    rationale = parse_obs_field(doc, "rationale")
    scope     = parse_obs_field(doc, "scope")

    label  = {"decision": "Decision", "fact": "Fact"}.get(obs_type, "Memory")
    header = f"[{label}] {topic}"
    if scope:
        header += f" — {scope}"
    parts = [header, claim]
    if rationale:
        parts.append(f"Reason: {rationale}")
    return "\n".join(parts)

mcp_server = Server("lazy-memory")
sse = SseServerTransport("/mcp/messages/")


@mcp_server.list_tools()
async def list_tools() -> list[types.Tool]:
    return [
        types.Tool(
            name="search_memories",
            description="Search past Claude Code sessions for decisions and domain facts. Returns formatted memory entries most similar to the query.",
            inputSchema={
                "type": "object",
                "properties": {
                    "query": {
                        "type": "string",
                        "description": "What to search for, e.g. 'fixing authentication bug' or 'refactoring the proxy handler'",
                    },
                    "n_results": {
                        "type": "integer",
                        "description": "Number of results to return (default 5)",
                        "default": 5,
                    },
                    "include_none": {
                        "type": "boolean",
                        "description": "Include type=none observations (default false — only decisions and facts)",
                        "default": False,
                    },
                },
                "required": ["query"],
            },
        )
    ]


@mcp_server.call_tool()
async def call_tool(name: str, arguments: dict) -> list[types.TextContent]:
    if name != "search_memories":
        raise ValueError(f"Unknown tool: {name}")

    query = arguments["query"]
    n = int(arguments.get("n_results", 5))
    include_none = bool(arguments.get("include_none", False))

    count = collection.count()
    if count == 0:
        return [types.TextContent(type="text", text="[]")]

    where_filter = None if include_none else {"obs_type": {"$in": ["decision", "fact"]}}
    query_kwargs: dict = {"query_texts": [query], "n_results": min(n, count)}
    if where_filter:
        query_kwargs["where"] = where_filter

    try:
        results = collection.query(**query_kwargs)
    except Exception:
        return [types.TextContent(type="text", text="[]")]

    out = []
    for i, doc in enumerate(results["documents"][0]):
        meta = results["metadatas"][0][i] or {}
        out.append({
            "session_id":    results["ids"][0][i],
            "memory":        format_memory(doc, meta),
            "obs_type":      meta.get("obs_type", ""),
            "created_at_ms": meta.get("created_at_ms", 0),
        })
    return [types.TextContent(type="text", text=json.dumps(out, indent=2))]


async def handle_sse(request: Request):
    async with sse.connect_sse(
        request.scope, request.receive, request._send
    ) as streams:
        await mcp_server.run(
            streams[0], streams[1], mcp_server.create_initialization_options()
        )


async def handle_ingest(request: Request):
    try:
        data = await request.json()
    except Exception:
        return JSONResponse({"error": "invalid JSON"}, status_code=400)

    session_id = data.get("session_id", "")
    summary = data.get("summary", "")
    if not session_id or not summary:
        return JSONResponse({"error": "session_id and summary are required"}, status_code=400)

    obs_type = parse_obs_field(summary, "type")
    if not obs_type:
        return JSONResponse({"ok": True, "skipped": True})

    collection.upsert(
        ids=[session_id],
        documents=[summary],
        metadatas=[{
            "model":         data.get("model", ""),
            "format":        data.get("format", ""),
            "token_count":   int(data.get("token_count", 0)),
            "created_at_ms": int(data.get("created_at_ms", 0)),
            "obs_type":      obs_type,
        }],
    )
    return JSONResponse({"ok": True})


async def handle_search(request: Request):
    try:
        data = await request.json()
    except Exception:
        return JSONResponse({"text": "", "ids": []})

    query = data.get("query", "")
    threshold = float(data.get("threshold", DEFAULT_THRESHOLD))
    n = int(data.get("n", 5))
    exclude_ids = set(data.get("exclude_ids", []))

    if not query:
        return JSONResponse({"text": "", "ids": []})

    log.debug("search query=%r threshold=%.2f n=%d exclude=%d", query, threshold, n, len(exclude_ids))

    count = collection.count()
    if count == 0:
        return JSONResponse({"text": "", "ids": []})

    try:
        fetch_n = min(n + len(exclude_ids), count)
        results = collection.query(
            query_texts=[query],
            n_results=fetch_n,
            where={"obs_type": {"$in": ["decision", "fact"]}},
            include=["documents", "metadatas", "distances"],
        )
    except Exception:
        return JSONResponse({"text": "", "ids": []})

    lines = []
    matched_ids = []
    for i, doc in enumerate(results["documents"][0]):
        doc_id = results["ids"][0][i]
        distance = results["distances"][0][i]
        meta = results["metadatas"][0][i] or {}
        topic = parse_obs_field(doc, "topic") or "(no topic)"
        excluded = doc_id in exclude_ids
        log.debug(
            "  candidate id=%s distance=%.4f topic=%r excluded=%s",
            doc_id, distance, topic, excluded,
        )
        if excluded or distance > threshold:
            continue
        lines.append(format_memory(doc, meta))
        matched_ids.append(doc_id)

    log.info("search returned %d/%d results ids=%s", len(matched_ids), fetch_n, matched_ids)

    if not lines:
        return JSONResponse({"text": "", "ids": []})

    return JSONResponse({
        "text": "Relevant past decisions from previous sessions:\n\n" + "\n\n".join(lines),
        "ids": matched_ids,
    })


async def handle_health(request: Request):
    return JSONResponse({"ok": True, "sessions": collection.count()})


class _SentResponse(Response):
    async def __call__(self, scope, receive, send):
        pass  # response already written by sse.handle_post_message via _send


async def handle_post_message(request: Request):
    # SseServerTransport.handle_post_message is an ASGI callable (scope, receive, send),
    # not a Starlette endpoint — it writes the response directly via _send.
    await sse.handle_post_message(request.scope, request.receive, request._send)
    return _SentResponse()


app = Starlette(
    routes=[
        Route("/health", handle_health),
        Route("/ingest", handle_ingest, methods=["POST"]),
        Route("/search", handle_search, methods=["POST"]),
        Route("/mcp/sse", handle_sse),
        Route("/mcp/messages/", handle_post_message, methods=["POST"]),
    ]
)

if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=PORT)
