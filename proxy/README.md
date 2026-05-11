# lazy-vllm-proxy

A lightweight reverse proxy that routes OpenAI-compatible and Anthropic API requests to one or more vLLM backends.

## What it does

- Accepts `/v1/chat/completions` (OpenAI format) and `/v1/messages` (Anthropic format) endpoints
- Routes requests to upstream vLLM instances based on model name prefixes
- Captures request/response payloads to disk for debugging and observability
- Provides a web UI to browse captured logs

## Quick start

```bash
export BACKENDS_MAP='[{"prefix":"gemma","url":"http://localhost:8000"},{"prefix":"qwen","url":"http://localhost:8001"}]'
export PORT=8002
export LOG_DIR=./logs          # optional: enable disk logging
./lazy-vllm-proxy
```

Then point any OpenAI/Anthropic SDK client at `http://localhost:8002`.

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `BACKENDS_MAP` | JSON array of `{prefix, url}` routing rules | **required** |
| `PORT` | HTTP listen port | `8002` |
| `LOG_DIR` | Directory for persisted request/response logs (SQLite DB) | none (disabled) |
| `LOG_LEVEL` | Log verbosity: DEBUG, INFO, WARN, ERROR | `INFO` |

`BACKENDS_MAP` is tried in order — the first rule whose `prefix` matches the request model wins.

## Logging

Captured logs are stored in a SQLite database (`logs.db`) using the pure-Go `modernc.org/sqlite` driver — no CGo required.
The schema uses versioned migrations via `PRAGMA user_version` so it can evolve without manual upgrades.
WAL mode is enabled for concurrent reads during writes.

When `LOG_DIR` is set, every request is indexed by `started_at` and queryable via the `/api/logs` and `/api/logs/{id}` endpoints,
which serve the JSON consumed by the web UI at `/ui/logs`.

## Endpoints

| Path | Method | Description |
|------|--------|-------------|
| `/v1/chat/completions` | POST | OpenAI-compatible chat completions |
| `/v1/messages` | POST | Anthropic-compatible messages |
| `/health` | GET | Health check (200 OK) |
| `/metrics` | GET | Prometheus metrics |
| `/log-level` | POST | Runtime log level change (`{"level":"DEBUG"}`) |
| `/ui/logs` | GET | Web UI for browsing captured logs |
| `/api/logs` | GET | JSON list of captured log IDs |
| `/api/logs/{id}` | GET | JSON detail of a specific log |
