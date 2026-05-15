# Lazy-vLLM

A local LLM infrastructure stack built around Qwen models — routing proxy, observability, logging, and a sandboxed Claude Code environment, all runnable in one command.

## What it solves

1. **Smart routing** — A proxy layer over multiple vLLM backends for fast model selection, context-size-aware routing, and instant flash responses.
2. **Observability** — Grafana dashboards for model metrics, host metrics, GPU metrics, and Docker container metrics.
3. **Message logging** — Full request/response capture with a web UI for debugging and visibility into every LLM interaction.
4. **Sandboxed coding** — Claude Code running locally against your own models, isolated in Docker.
5. **One-command deploy** — `docker compose up -d` starts the entire stack.

## Architecture

```
┌─────────────┐    ┌──────────────┐    ┌──────────────────────────────────┐
│  Open-WebUI │───▶│  Proxy (Go)  │───▶│  vLLM: Qwen3.6-27B (reasoning)  │
│             │    │              │    │  vLLM: Qwen3.6-35B-A4B (flash)   │
│  Client     │────▶│  Agent Graph │───▶│  vLLM: Qwen3.6-35B-A4B (reasoning)│
└─────────────┘    └──────────────┘    └──────────────────────────────────┘
                           │
                    ┌──────▼───────┐
                    │  Prometheus │
                    │  Grafana    │
                    └─────────────┘
```

## Core Services

| Service | Port | Description |
|---------|------|-------------|
| **Proxy** | 8002 | Reverse proxy with flash models, compact logging, token-based routing |
| **Agent Graph** | 8002 | Multi-step agent pipelines with classify→respond graphs |
| **Open-WebUI** | 3000 | Chat interface |
| **Grafana** | 3001 | Observability dashboards |
| **Prometheus** | 9090 | Metrics collection |
| **claude-local** | — | Claude Code in Docker, pointed at local models |

## Quick Start

```bash
# Start everything
docker compose up -d

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

## 📂 Project Structure

```
.
├── docker-compose.yml         # Orchestration — starts all services
├── Dockerfile.gemma4          # vLLM engine build (Qwen models)
├── Dockerfile.claude          # Claude Code container
├── Dockerfile.opencode        # Opencode service
├── scripts/                   # Install, uninstall, entrypoint scripts
├── proxy/                      # Routing proxy (Go) — flash, logging, token routing
├── agent/                      # Agent Graph Framework (Go)
├── router/                     # Thinking-Router (Go)
├── opencode/                   # Opencode service
├── grafana/                    # Grafana dashboards
├── prometheus/                 # Prometheus config
├── synthetic-ui/              # Data labeling UI
└── assets/                      # Screenshots and media
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

docker compose up -d
```

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
