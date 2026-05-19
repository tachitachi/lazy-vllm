import json
import os

import chromadb
import uvicorn
import mcp.types as types
from mcp.server import Server
from mcp.server.sse import SseServerTransport
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse
from starlette.routing import Route

CHROMA_PATH = os.getenv("CHROMA_PATH", "/data")
PORT = int(os.getenv("PORT", "8020"))

chroma = chromadb.PersistentClient(path=CHROMA_PATH)
collection = chroma.get_or_create_collection(
    "sessions",
    metadata={"hnsw:space": "cosine"},
)

mcp_server = Server("lazy-memory")
sse = SseServerTransport("/mcp/messages/")


@mcp_server.list_tools()
async def list_tools() -> list[types.Tool]:
    return [
        types.Tool(
            name="search_memories",
            description="Search past Claude Code sessions semantically. Returns sessions whose summaries are most similar to the query.",
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

    count = collection.count()
    if count == 0:
        return [types.TextContent(type="text", text="[]")]

    results = collection.query(query_texts=[query], n_results=min(n, count))
    out = []
    for i, doc in enumerate(results["documents"][0]):
        out.append(
            {
                "session_id": results["ids"][0][i],
                "summary": doc,
                **(results["metadatas"][0][i] or {}),
            }
        )
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

    collection.upsert(
        ids=[session_id],
        documents=[summary],
        metadatas=[
            {
                "model": data.get("model", ""),
                "format": data.get("format", ""),
                "token_count": int(data.get("token_count", 0)),
                "created_at_ms": int(data.get("created_at_ms", 0)),
            }
        ],
    )
    return JSONResponse({"ok": True})


async def handle_health(request: Request):
    return JSONResponse({"ok": True, "sessions": collection.count()})


async def handle_post_message(request: Request):
    # SseServerTransport.handle_post_message is an ASGI callable (scope, receive, send),
    # not a Starlette endpoint, so we must unwrap and forward.
    await sse.handle_post_message(request.scope, request.receive, request._send)


app = Starlette(
    routes=[
        Route("/health", handle_health),
        Route("/ingest", handle_ingest, methods=["POST"]),
        Route("/mcp/sse", handle_sse),
        Route("/mcp/messages/", handle_post_message, methods=["POST"]),
    ]
)

if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=PORT)
