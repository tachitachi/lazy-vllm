# agent-graph

A Go framework for building and executing **agentic workflows** on top of vLLM. Define models, tools, and reasoning loops as composable graph nodes — the runtime handles tool execution, parallel scatter-gather, and thinking management automatically.

## What it does

- Define multi-step agent workflows as directed graphs with typed nodes
- Automatically orchestrate tool loops, parallel branches, and conditional routing
- Enforce [Gemma4 thinking rules](https://ai.google.dev/gemma/docs/capabilities/thinking#multi-turn_example_with_thought_stripping) — strips thinking from prior turns, preserves it within tool calls, commits clean responses to history

## Graph node types

| Kind | Behaviour |
|------|-----------|
| **Route** | Calls the model once, captures a string output, leaves message history untouched |
| **Chain** | Runs a tool loop internally — the model iterates with tool calls until it produces a final response |
| **Scatter** | Fans out to N parallel branches (each with its own sub-graph), then merges results |
| **Respond** | Rebuilds an API request from the current state and streams the result back |

Edges between nodes can carry conditions (`output == "REASONING" ?`) and transforms (mutate the pipeline state before the next node runs).

## Quick start

```bash
export PROXY_URL=http://localhost:8002
export MODEL_NAME=your-model
export API_KEY=sk-...
./agent thinking-router "Explain quantum computing"
```

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `PROXY_URL` | Address of the lazy-vllm-proxy | **required** |
| `MODEL_NAME` | Default model to call | **required** |
| `API_KEY` | Bearer token forwarded to the proxy | none |
| `ROUTER_WINDOW_SIZE` | Message history window (0 = unlimited) | `0` |
| `LOG_LEVEL` | DEBUG, INFO, WARN, ERROR | `INFO` |

## Available agents

```bash
./agent --help
```

Currently registered:

- **`thinking-router`** — Classifies the prompt as `DIRECT` or `REASONING` (structured output), then enables thinking mode accordingly before responding.
- **`assistant`** — Simple single-turn assistant with no routing.

## Extending

Graph factories live in `internal/graphs/registry.go`. Add a new agent by:

1. Writing a `func(cfg agent.Config) *agent.Graph` that defines nodes and edges
2. Registering it in the `Registry` map

See `agent/DESIGN.md` for the full architecture, type definitions, and example graph definitions.
