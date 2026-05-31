# Lazy-vLLM

A local LLM infrastructure stack built around Qwen models — routing proxy, observability, logging, and a sandboxed Claude Code environment, all runnable in one command.

## What it solves

1. **Smart routing** — A proxy layer over multiple vLLM backends for fast model selection, context-size-aware routing, and instant flash responses.
2. **Observability** — Grafana dashboards for model metrics, host metrics, GPU metrics, and Docker container metrics.
3. **Message logging** — Full request/response capture with a web UI for debugging and visibility into every LLM interaction.
4. **Semantic memory** — The proxy extracts a one-sentence observation from every session and embeds it into ChromaDB. An MCP server exposes `search_memories` so Claude Code can retrieve relevant past sessions by semantic similarity.
5. **Sandboxed coding** — Claude Code running locally against your own models, isolated in Docker.
6. **One-command deploy** — `docker compose up -d` starts the entire stack.

## Architecture

```
┌─────────────┐    ┌──────────────┐    ┌──────────────────────────────────┐
│  Open-WebUI │───▶│  Proxy (Go)  │───▶│  vLLM: Qwen3.6-35B-A4B (flash)   │
│             │    │  :8002       │    │  vLLM: Qwen3.6-35B-A4B (1M ctx)  │
│  Client     │────▶│  Agent Graph │───▶│  vLLM: Qwen3.6-27B               │
└─────────────┘    └──────┬───────┘    └──────────────────────────────────┘
                           │ route over Tailscale        ▲
                           │              ┌───────────────┴──┐
                    ┌──────▼───────┐       │  fast-qwen      │       ┌──────────────────────┐
                    │  SQLite      │       │  vLLM (bare     │◀──────│  Windows Host        │
                    │  (sessions)  │       │  metal)          │       │  RTX 5090            │
                    └──────────────┘       │  Qwen3.6-27B     │       │  Qwen3.6-27B-Text    │
                           │                │  :8000           │       │  (NVFP4-MTP)         │
                    ┌──────────────┐       └──────────────────┘       └──────────────────────┘
                    │  Prometheus  │
                    │  Grafana     │
                    └─────────────┘
```

## Core Services

| Service | Port | Description |
|---------|------|-------------|
| **Proxy** | 8002 | Reverse proxy with flash models, compact logging, token-based routing |
| **Agent Graph** | 8002 | Multi-step agent pipelines with classify→respond graphs |
| **Memory** | 8020 | Semantic memory: ChromaDB embeddings + MCP SSE server for `search_memories` |
| **Open-WebUI** | 3000 | Chat interface |
| **Grafana** | 3001 | Observability dashboards |
| **fast-qwen** | 8000 | Remote Qwen3.6-27B on Windows/5090 (Tailscale) |
| **Prometheus** | 9090 | Metrics collection |
| **claude-local** | — | Claude Code in Docker, pointed at local models |

## Remote Fast Model (fast-qwen)

A separate Windows host running a RTX 5090 serves Qwen3.6-27B bare-metal via `vllm-windows`. It joins the stack over Tailscale as `fast-qwen`, letting the proxy route requests to it like any local backend.

### Setup (Windows host)

```bash
# Install vllm-windows per https://github.com/SystemPanic/vllm-windows
# Then run:
./run-vllm-windows.sh
```

### Setup (compose host)

```bash
export FAST_QWEN_HOST=<tailscale-hostname-of-5090-machine>
./scripts/docker-compose.sh up -d
```

The wrapper resolves `FAST_QWEN_HOST`'s Tailscale IP via `tailscale ip --4=true` (or `dig`), exports `FAST_QWEN_IP`, and passes it through `extra_hosts` so the proxy and Prometheus can reach `fast-qwen:8000`. The actual hostname never appears in the compose file.

### Future

Dynamic routing will fall through to slower local backends when `fast-qwen` is saturated, letting the faster host handle latency-sensitive requests while the slower host absorbs concurrency spikes.

## Quick Start

```bash
# Set the Tailscale hostname of your fast-qwen node
export FAST_QWEN_HOST=<tailscale-hostname>

# Start everything
./scripts/docker-compose.sh up -d

# Run Claude Code locally
claude-local "explain this codebase"
```

## Routing Proxy

The proxy (`proxy/`) sits in front of one or more vLLM backends and provides:

- **Flash models** — Every model gets an automatic `-FLASH` variant that disables thinking for instant responses.
- **Token-based routing** — Large requests can be upgraded to different models based on token thresholds.
- **Compact logging** — O(n) storage via global message deduplication, per-session tool tracking, and message timing.
- **Catch-all proxy** — Unrecognized paths forward transparently to the backend resolved by the `model` field.

### Configuration

```bash
export BACKENDS_MAP='[{"name":"qwen-35b-flash","url":"http://vllm-flash:8000"},{"name":"qwen-27b","url":"http://vllm-reasoning:8000"}]'
export ROUTING_RULES='[{"source_model":"qwen-35b-flash","threshold":32000,"target_model":"qwen-27b"}]'
```

The proxy is configured to route to `fast-qwen` (Qwen3.6-27B on a remote Windows/5090 node) by default. The `BACKENDS_MAP` env var in `docker-compose.yml` maps model names to backend URLs.

### Future: Load Balancing

The current setup routes to `fast-qwen` for low-latency inference. Future routing rules will fall through to slower local backends when `fast-qwen` is saturated, letting the faster host handle latency-sensitive requests while the slower host absorbs concurrency spikes.

See [proxy/README.md](proxy/README.md) for full details.

### Logging UI

![Agent Logs UI](assets/agent_logs.png)

The proxy captures every request and serves it through an embedded web UI at `/ui/logs` with:
- Session list showing format, model, tokens, and response time
- Expandable input messages, tools, and output with reasoning blocks
- Duration tracking per message

## Observability

The stack includes Prometheus + Grafana for:
- **Model metrics**: Token throughput, latency, error rates
- **Hardware metrics**: GPU temperature, VRAM utilization, power draw
- **Docker metrics**: Container CPU/memory via cAdvisor
- **System metrics**: Host resources via node-exporter

Dashboards at [http://localhost:3001](http://localhost:3001).

## Claude Local

`claude-local` runs Claude Code inside Docker, pointed at your local model stack:

```bash
# Install
./scripts/install.sh

# Use from any directory
cd ~/my-project
claude-local "refactor this module"
```

Config (`~/.claude/`) persists in a Docker named volume. See [🐳 Claude Local](#-claude-local-docker-based-claude-code) below for details.

## 🤖 Agent Graph Framework

The `agent/` module is a code-defined agent execution engine. See [Agent Graph Framework](#-agent-graph-framework) below for primitives and examples.

## 🧠 Semantic Memory

Every session routed through the proxy produces an `<obs>` block — a 1–3 sentence summary of what was asked and what was done. The proxy strips it from the response (the client never sees it) and stores it in two places:

1. **SQLite** — `sessions.summary` column alongside the full message history
2. **ChromaDB** — embedded with `all-MiniLM-L6-v2` for semantic search

Claude Code can query past sessions via an MCP tool:

```
search_memories("authentication bug in proxy handler")
→ [{ session_id, summary, model, token_count, created_at_ms }, ...]
```

### Setup

The memory service starts automatically with `docker compose up -d`. The `.mcp.json` at the project root pre-registers it with Claude Code:

```json
{
  "mcpServers": {
    "lazy-memory": { "type": "sse", "url": "http://localhost:8020/mcp/sse" }
  }
}
```

After Claude Code approves the server (one-time prompt), `search_memories` is available in every session within this project.

See [memory/README.md](memory/README.md) for full architecture details.

## 📂 Project Structure

```
.
├── docker-compose.yml         # Orchestration — starts all services
├── Dockerfile.gemma4          # vLLM engine build (Qwen models)
├── Dockerfile.claude          # Claude Code container
├── Dockerfile.opencode        # Opencode service
├── .mcp.json                  # MCP server registration for Claude Code
├── run-vllm-windows.sh        # Bare-metal vLLM launcher (Windows/5090 host)
├── scripts/                   # Install, uninstall, entrypoint scripts
├── scripts/docker-compose.sh  # Tailscale IP resolver for fast-qwen
├── proxy/                     # Routing proxy (Go) — flash, logging, token routing
├── memory/                    # Semantic memory service (Python) — ChromaDB + MCP
├── agent/                     # Agent Graph Framework (Go)
├── router/                    # Thinking-Router (Go)
├── opencode/                  # Opencode service
├── grafana/                   # Grafana dashboards
├── prometheus/                # Prometheus config
├── synthetic-ui/              # Data labeling UI
└── assets/                    # Screenshots and media
```

---

## 🧪 Synthetic UI

See [🧪 Synthetic UI](#-synthetic-ui-data-labeling-tool) below for details.

## 🐳 Claude Local (Docker-based Claude Code)

`claude-local` runs Claude Code inside a Docker container pointed at your local model stack instead of Anthropic's API. It behaves identically to the `claude` CLI — same flags, same workspace, same MCP tools — but routes all inference through `localhost:8002`.

### Install

```bash
./scripts/install.sh
```

This builds the `claude-local-dev` Docker image and creates a `claude-local` symlink in `~/.local/bin`. If `~/.local/bin` is not in your `PATH`, the script will remind you to add it:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

### Usage

```bash
cd ~/my-project
claude-local

claude-local --help
claude-local "explain this codebase"
claude-local --model claude-sonnet-4-6 "review my changes"
```

### Configuration persistence

Claude config (`~/.claude/`, `~/.claude.json`) is stored in a Docker named volume (`claude-home`) so settings and workspace trust answers survive container restarts.

### Uninstall

```bash
./scripts/uninstall.sh

# Optional: remove image and volumes
docker rmi claude-local-dev
docker volume rm claude-home claude-npm-cache claude-cache claude-go-cache
```

---

## 🛠️ Getting Started

### Prerequisites

- [Docker](https://docs.docker.com/get-docker/)
- [Docker Compose](https://docs.docker.com/compose/install/)
- NVIDIA GPU with compatible drivers
- [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)

### Deployment

```bash
git clone <repository-url>
cd lazy-vllm

export OPENCODE_SERVER_PASSWORD=your_password
export FAST_QWEN_HOST=<tailscale-hostname-of-5090-machine>

./scripts/docker-compose.sh up -d
```

The `docker-compose.sh` wrapper resolves `FAST_QWEN_HOST`'s Tailscale IP and injects it into the stack. Without it, the fast-qwen backend will not be reachable.

### Accessing the Services

- **Web Interface**: [http://localhost:3000](http://localhost:3000) (Open-WebUI)
- **Agent Graph API**: [http://localhost:8002](http://localhost:8002)
- **Grafana Dashboards**: [http://localhost:3001](http://localhost:3001)
- **Prometheus Metrics**: [http://localhost:9090](http://localhost:9090)

---

## 🤖 Agent Graph Framework

The `agent/` module is a code-defined agent execution engine built around four concepts:

| Primitive | Purpose |
|-----------|---------|
| **Agent** | A model identity — system prompt, model name, parameters, tools, and history policy |
| **Node** | An agent with a role: `Route` (classify), `Chain` (tool loop), `Scatter` (parallel fan-out), or `Respond` (stream back to client) |
| **Edge** | A conditional transition between nodes with an optional transform applied to shared state |
| **PipelineCtx** | The shared mutable state flowing through the graph — message history, node outputs, overrides |

Both `POST /v1/chat/completions` (OpenAI) and `POST /v1/messages` (Anthropic) are supported. The graph runs internally in OpenAI format; format translation happens only at the boundaries.

### Examples

#### 1. Classify → Respond

```go
graph := &agent.Graph{
    Entry: "classify",
    Cfg:   cfg,
    Nodes: map[string]*agent.Node{
        "classify": {
            ID:   "classify",
            Kind: agent.NodeKindRoute,
            Agent: agent.Agent{
                SystemPrompt:     routingPrompt,
                MaxTokens:        16,
                ThinkingMode:     boolPtr(false),
                StructuredChoice: []string{"DIRECT", "REASONING"},
            },
        },
        "respond": {ID: "respond", Kind: agent.NodeKindRespond},
    },
    Edges: []agent.Edge{
        {
            From: "classify",
            To:   "respond",
            Transform: func(pctx *agent.PipelineCtx) {
                thinking := pctx.NodeOutputs["classify"] == "REASONING"
                pctx.ThinkingMode = &thinking
            },
        },
    },
}
```

#### 2. Tool-using Chain agent

```go
graph := &agent.Graph{
    Entry: "research",
    Cfg:   cfg,
    Nodes: map[string]*agent.Node{
        "research": {
            ID:   "research",
            Kind: agent.NodeKindChain,
            Agent: agent.Agent{
                SystemPrompt: "You are a research assistant. Use tools to answer the question.",
                Tools:        []agent.ToolDef{webSearchTool},
                History:      agent.HistoryPolicy{StripThinkingOnCommit: true},
            },
        },
        "respond": {ID: "respond", Kind: agent.NodeKindRespond},
    },
    Edges: []agent.Edge{{From: "research", To: "respond"}},
}
```

#### 3. Scatter-gather (parallel sub-agents)

```go
graph := &agent.Graph{
    Entry: "orchestrate",
    Cfg:   cfg,
    Nodes: map[string]*agent.Node{
        "orchestrate": {ID: "orchestrate", Kind: agent.NodeKindChain,
            Agent: agent.Agent{SystemPrompt: `Decompose the request. Output JSON array of tasks.`}},
        "explore":    {ID: "explore", Kind: agent.NodeKindScatter},
        "synthesize": {ID: "synthesize", Kind: agent.NodeKindChain,
            Agent: agent.Agent{SystemPrompt: "Synthesize the results."}},
        "respond": {ID: "respond", Kind: agent.NodeKindRespond},
    },
    Scatter: map[string]agent.ScatterConfig{
        "explore": {BranchFactory: func(pctx *agent.PipelineCtx) []agent.Branch { /* ... */ },
                    Merge: func(parent *agent.PipelineCtx, branches []*agent.PipelineCtx) { /* ... */ }},
    },
    Edges: []agent.Edge{
        {From: "orchestrate", To: "explore"},
        {From: "explore", To: "synthesize"},
        {From: "synthesize", To: "respond"},
    },
}
```

---

## 🧪 Synthetic UI (Data Labeling Tool)

The `synthetic-ui` is a specialized web interface for reviewing and labeling synthetic conversation data.

### Getting Started

```bash
cd synthetic-ui
npm run install-all
npm run dev
```

- **Web Interface**: [http://localhost:5173](http://localhost:5173)
- **API Server**: [http://localhost:3003](http://localhost:3003)

---

## 📊 Monitoring

The included Grafana dashboards allow you to monitor:
- **Router Performance**: Classification latency, request throughput, and error rates.
- **Model Performance**: Token generation speed and vLLM utilization.
- **Hardware Health**: GPU temperature, memory utilization, and system-wide resource consumption.
