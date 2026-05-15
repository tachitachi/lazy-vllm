# Lazy-vLLM: Intelligent Reasoning Orchestration for Gemma 4

Lazy-vLLM is a high-performance orchestration environment designed to maximize the efficiency of reasoning-capable LLMs, specifically the **Gemma 4** family. It provides two complementary components: a lightweight **Thinking Router** for simple classify-and-proxy workflows, and a general-purpose **Agent Graph Framework** for building multi-step agentic pipelines.

## 🚀 Overview

The core philosophy of Lazy-vLLM is to avoid using expensive reasoning capabilities for trivial tasks. The system intelligently classifies queries and dynamically enables or disables the model's "thinking" mode (chain-of-thought) based on the complexity of the request.

### Architecture Components

- **Thinking-Router (Go)**: A lightweight proxy that:
    - Classifies incoming requests as `DIRECT` or `REASONING`.
    - Dynamically injects `chat_template_kwargs` to control the model's reasoning behavior.
    - Supports both OpenAI `/v1/chat/completions` and Anthropic `/v1/messages` API formats.
    - Strips prior thinking blocks from message history per Gemma 4's multi-turn requirements.
- **Lazy-vLLM Proxy (Go)**: A lightweight reverse proxy that routes requests to upstream vLLM instances by model name prefix, captures logs to disk (SQLite), and provides a web UI for observability ([details](proxy/README.md)).
    - **Flash models**: Every model automatically gets a `-FLASH` variant that disables thinking for instant responses.
    - **Compact logging**: O(n) storage via global message deduplication and per-session tool tracking.
    - **Token-based routing**: Automatically route large requests to different backends based on token thresholds.
- **Agent Graph Framework (Go)**: A general-purpose agent execution engine (see [Agent Graph Framework](#-agent-graph-framework) below).
- **vLLM (Gemma 4 Engine)**: The high-throughput inference backend running `google/gemma-4-26B-A4B-it`.
- **Open-WebUI**: A feature-rich, user-friendly web interface for interacting with the LLM.
- **Opencode**: A specialized service for executing tasks within a controlled workspace environment.
- **Observability Stack**: 
    - **Prometheus**: Collects metrics from the router and system hardware.
    - **Grafana**: Provides real-time visualization of performance and resource utilization.
    - **Exporters**: Includes `nvidia-gpu-exporter`, `node-exporter`, and `cadvisor` for deep hardware insights.

### Gemma 4 Engine Specializations
The `gemma4` service uses a specialized build via `Dockerfile.gemma4` that includes a critical patch for reasoning parsing. This patch (found in `patches/gemma4_reasoning_parser.py`) fixes a known issue in vLLM where reasoning content could leak into the main content channel during multi-turn tool-use streaming on Gemma 4 MoE models.

## 🛠️ Getting Started

### Prerequisites

- [Docker](https://docs.docker.com/get-docker/)
- [Docker Compose](https://docs.docker.com/compose/install/)
- NVIDIA GPU with compatible drivers
- [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)

### Deployment

1. Clone the repository:
   ```bash
   git clone <repository-url>
   cd lazy-vllm
   ```

2. Configure your environment variables (e.g., API keys for external providers if used):
   ```bash
   export OPENCODE_SERVER_PASSWORD=your_password
   # Add other keys as needed (ANTHROPIC_API_KEY, etc.)
   ```

3. Launch the stack:
   ```bash
   docker compose up -d
   ```

### Accessing the Services

- **Web Interface**: [http://localhost:3000](http://localhost:3000) (Open-WebUI)
- **Thinking Router API**: [http://localhost:8001](http://localhost:8001)
- **Agent Graph API**: [http://localhost:8002](http://localhost:8002)
- **Grafana Dashboards**: [http://localhost:3001](http://localhost:3001)
- **Prometheus Metrics**: [http://localhost:9090](http://localhost:9090)

## 🤖 Agent Graph Framework

The `agent/` module is a code-defined agent execution engine built around four concepts:

| Primitive | Purpose |
|-----------|---------|
| **Agent** | A model identity — system prompt, model name, parameters, tools, and history policy |
| **Node** | An agent with a role: `Route` (classify), `Chain` (tool loop), `Scatter` (parallel fan-out), or `Respond` (stream back to client) |
| **Edge** | A conditional transition between nodes with an optional transform applied to shared state |
| **PipelineCtx** | The shared mutable state flowing through the graph — message history, node outputs, overrides |

Both `POST /v1/chat/completions` (OpenAI) and `POST /v1/messages` (Anthropic) are supported. The graph runs internally in OpenAI format; format translation happens only at the boundaries.

### Gemma 4 Thinking Rules

The framework enforces Gemma 4's thinking-strip requirements automatically:

- **Cross-turn**: `pctx.Messages` always holds stripped history — thinking is never present across turn boundaries.
- **Tool loop exception**: Within a `Chain` node's tool loop, `reasoning` content is preserved in the node's internal `workingMessages` so the model can reference its own reasoning when processing tool results.
- **On commit**: When a `Chain` node finishes its loop, set `HistoryPolicy.StripThinkingOnCommit = true` to zero out `reasoning` before writing the final response to `pctx.Messages`.

### Examples

#### 1. Classify → Respond (mirrors the Thinking Router)

The default graph shipped with `agent/main.go`. A `Route` node classifies the request; an edge transform sets `ThinkingMode`; a `Respond` node proxies to vLLM and streams back.

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

A `Chain` node runs a tool loop until the model stops calling tools, then commits the final response to `pctx.Messages` with thinking stripped. A `Respond` node streams the result.

```go
webSearchTool := agent.ToolDef{
    Name:        "web_search",
    Description: "Search the web for current information.",
    Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
    Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
        var args struct{ Query string `json:"query"` }
        json.Unmarshal(input, &args)
        return search(args.Query) // your implementation
    },
}

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
    Edges: []agent.Edge{
        {From: "research", To: "respond"},
    },
}
```

#### 3. Scatter-gather (parallel sub-agents)

An orchestrator `Chain` node outputs a JSON list of tasks. A `Scatter` node fans out — one branch per task, each running an independent sub-graph. A `Merge` function folds the results back into `pctx.Messages`. A final `Chain` node synthesizes the results.

```go
graph := &agent.Graph{
    Entry: "orchestrate",
    Cfg:   cfg,
    Nodes: map[string]*agent.Node{
        "orchestrate": {
            ID:   "orchestrate",
            Kind: agent.NodeKindChain,
            Agent: agent.Agent{
                SystemPrompt: `Decompose the user's request into independent research tasks.
Output a JSON array of task strings, e.g. ["task 1", "task 2"].`,
                History: agent.HistoryPolicy{StripThinkingOnCommit: true},
            },
        },
        "explore":    {ID: "explore", Kind: agent.NodeKindScatter},
        "synthesize": {
            ID:   "synthesize",
            Kind: agent.NodeKindChain,
            Agent: agent.Agent{
                SystemPrompt: "Synthesize the research results into a comprehensive answer.",
                History:      agent.HistoryPolicy{StripThinkingOnCommit: true},
            },
        },
        "respond": {ID: "respond", Kind: agent.NodeKindRespond},
    },
    Scatter: map[string]agent.ScatterConfig{
        "explore": {
            BranchFactory: func(pctx *agent.PipelineCtx) []agent.Branch {
                var tasks []string
                json.Unmarshal([]byte(pctx.NodeOutputs["orchestrate"]), &tasks)

                branches := make([]agent.Branch, len(tasks))
                for i, task := range tasks {
                    b := pctx.DeepCopy()
                    b.Messages = append(b.Messages, agent.ChatMessage{
                        Role: "user", Content: "Research task: " + task,
                    })
                    branches[i] = agent.Branch{Ctx: b, SubGraph: exploreSubGraph}
                }
                return branches
            },
            Merge: func(parent *agent.PipelineCtx, branches []*agent.PipelineCtx) {
                var results []string
                for _, b := range branches {
                    results = append(results, b.NodeOutputs["explore_worker"])
                }
                parent.Messages = append(parent.Messages, agent.ChatMessage{
                    Role:    "user",
                    Content: "Research results:\n" + strings.Join(results, "\n---\n"),
                })
            },
        },
    },
    Edges: []agent.Edge{
        {From: "orchestrate", To: "explore"},
        {From: "explore", To: "synthesize"},
        {From: "synthesize", To: "respond"},
    },
}
```

### Project Structure

```text
agent/
├── DESIGN.md                  # Full architecture and design rationale
├── Dockerfile
├── go.mod
├── main.go                    # HTTP server + default classify→respond graph
├── log_ui.go                  # HTTP handlers for the logs UI
├── ui/
│   └── logs.html              # Embedded logs browser UI
└── internal/agent/
    ├── types.go               # Agent, Node, Edge, Graph, PipelineCtx, Branch
    ├── executor.go            # Graph.Run, node runners (Route/Chain/Scatter/Respond)
    ├── llm.go                 # vLLM HTTP client
    ├── history.go             # StripAllThinking, applyWindow, DeepCopy
    ├── disk_logger.go         # Request logging: capture, save, and retrieve full call traces
    ├── anthropic.go           # Anthropic ↔ internal format conversion
    ├── stream.go              # HTTP streaming helpers
    └── metrics.go             # Prometheus metrics
```

### Request Logging

The agent includes a built-in disk logger (`disk_logger.go`) that captures every request as JSON on disk, organized by date. Each log entry records:

- **Request metadata**: format (OpenAI/Anthropic), path, captured headers, full request body
- **All LLM calls**: for each node in the graph, the input messages, output (reasoning, content, tool calls), and timing
- **Timing**: total duration and per-call timestamps

The `log_ui.go` component serves an embedded logs browser (`ui/logs.html`) that provides:

- **Request list**: sidebar with format badge (OpenAI/Anthropic), duration, timestamp
- **Per-request detail**: collapsible call history showing input messages, reasoning blocks, tool calls, and outputs
- **Curl replay**: auto-generated curl command to replay the original request, with copy-to-clipboard
- **Auto-refresh**: toggleable 5-second polling for new logs

The logs UI is served at `/logs` with API endpoints at `/api/logs` (list) and `/api/logs/{id}` (detail) on the agent server.

## 🐳 Claude Local (Docker-based Claude Code)

`claude-local` runs Claude Code inside a Docker container pointed at your local model stack instead of Anthropic's API. It behaves identically to the `claude` CLI — same flags, same workspace, same MCP tools — but routes all inference through `localhost:8002`.

### Install

```bash
./scripts/install.sh
```

This builds the `claude-local-dev` Docker image and creates a `claude-local` symlink in `~/.local/bin`. If `~/.local/bin` is not in your `PATH`, the script will remind you to add it:

```bash
# Add to ~/.bashrc or ~/.zshrc if not already present
export PATH="$HOME/.local/bin:$PATH"
```

### Usage

Run `claude-local` from any directory — it mounts that directory as the workspace:

```bash
cd ~/my-project
claude-local
```

Arguments pass through to Claude Code:

```bash
claude-local --help
claude-local "explain this codebase"
claude-local --model claude-sonnet-4-6 "review my changes"
```

### Configuration persistence

Claude config (`~/.claude/`, `~/.claude.json`) is stored in a Docker named volume (`claude-home`) so your theme, settings, and workspace trust answers survive container restarts.

### Uninstall

```bash
./scripts/uninstall.sh
```

Removes the `claude-local` symlink. The Docker image and named volumes are left in place — remove them manually if desired:

```bash
docker rmi claude-local-dev
docker volume rm claude-home claude-npm-cache claude-cache claude-go-cache
```

---

## 🧪 Synthetic UI (Data Labeling Tool)

The `synthetic-ui` is a specialized web interface for reviewing and labeling synthetic conversation data generated by the system. It allows human-in-the-loop verification to ensure the quality of the reasoning traces.

### Features

- **Thread Review**: Browse through synthetic conversation threads.
- **Chat View**: Inspect the full message history, including user and assistant turns.
- **Labeling**: Apply labels to conversation pairs to categorize reasoning quality or intent.
- **Workflow Management**: Archive threads once reviewed or approve them to move them into the production-ready dataset.

### Getting Started

#### Prerequisites

- [Node.js](https://nodejs.org/) (LTS recommended)
- [npm](https://www.npmjs.com/)

#### Installation & Running

1. Navigate to the `synthetic-ui` directory:
   ```bash
   cd synthetic-ui
   ```

2. Install all dependencies (both server and client):
   ```bash
   npm run install-all
   ```

3. Launch the development environment:
   ```bash
   npm run dev
   ```

#### Accessing the UI

- **Web Interface**: [http://localhost:5173](http://localhost:5173)
- **API Server**: [http://localhost:3003](http://localhost:3003)

#### Configuration

The UI relies on environment variables for directory paths. You can configure these in a `.env` file within the `synthetic-ui` directory:

```env
SYNTHETIC_DIR=/path/to/your/synthetic/data
ARCHIVE_DIR=/path/to/your/archive/directory
APPROVED_DIR=/path/to/your/approved/directory
PORT=3003
```

By default, it looks for data in `../../router/data/synthetic` relative to the server directory.

## 🧠 How Routing Works

The `thinking-router` uses a small, fast classification pass to analyze the user's intent:

- **DIRECT**: Used for greetings, simple facts, or transformations. The router disables "thinking" to ensure minimal latency.
- **REASONING**: Used for logic, synthesis, or complex constraints. The router enables "thinking" to leverage the model's full cognitive capacity.

## 📊 Monitoring

The included Grafana dashboards allow you to monitor:
- **Router Performance**: Classification latency, request throughput, and error rates.
- **Model Performance**: Token generation speed and vLLM utilization.
- **Hardware Health**: GPU temperature, memory utilization, and system-wide resource consumption.

## 📂 Project Structure

```text
.
├── docker-compose.yml         # Main orchestration file
├── Dockerfile.gemma4          # Gemma 4 engine build definition
├── Dockerfile.claude          # Claude Code container build definition
├── Dockerfile.opencode        # Opencode service build definition
├── scripts/
│   ├── docker-run-claude.sh   # Claude Code container entrypoint
│   ├── install.sh             # Build image + install claude-local symlink
│   └── uninstall.sh           # Remove claude-local symlink
├── opencode/                  # Opencode service implementation
├── prometheus/                # Prometheus configuration
├── grafana/                   # Grafana provisioning and dashboards
├── proxy/                     # Lazy-vLLM Proxy (Go, port 8002) — flash models, compact logging, token routing
├── router/                    # Thinking-Router (Go, port 8001)
├── agent/                     # Agent Graph Framework (Go)
└── synthetic-ui/              # Synthetic UI for data labeling
```
