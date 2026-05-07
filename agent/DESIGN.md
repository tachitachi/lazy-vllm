# Agent Graph Framework

## Motivation

The original `router/` is a hardcoded two-node pipeline: a classifier decides whether to enable thinking, then one flag is injected before proxying to vLLM. Adding any new routing behaviour — a second classification tier, a tool-using agent, parallel research sub-agents — requires editing Go proxy logic directly.

This framework replaces that with a general execution graph. You define **agents** (model, system prompt, parameters, tools) and wire them into a **graph** of typed nodes. The executor handles tool loops, parallel scatter-gather, and Gemma4-compliant message history management automatically — none of that logic bleeds into the graph definition.

---

## Primitives

### Agent

A pure, stateless definition. No runtime state lives here.

```go
type Agent struct {
    SystemPrompt     string
    Model            string       // empty = inherit from Config
    MaxTokens        int
    ThinkingMode     *bool        // nil = inherit; true/false = force
    StructuredChoice []string     // forces structured_output.choice on the vLLM call
    Tools            []ToolDef
    History          HistoryPolicy
}

type ToolDef struct {
    Name        string
    Description string
    Parameters  json.RawMessage              // JSON Schema
    Handler     func(ctx context.Context, input json.RawMessage) (string, error)
}

type HistoryPolicy struct {
    StripThinkingOnCommit bool  // strip thinking before writing this turn to cross-turn history
    WindowSize            int   // 0 = no limit; applied before each model call
}
```

### PipelineCtx

The mutable state that flows through the graph. Each node reads and optionally writes it.

```go
type PipelineCtx struct {
    OriginalBody    []byte         // inbound request body, preserved for Respond node
    OriginalHeaders http.Header
    Stream          bool

    Messages    []ChatMessage      // cross-turn history; all prior turns have thinking stripped
    NodeOutputs map[string]string  // captured string outputs keyed by node ID

    ThinkingMode  *bool            // nil = resolve from Agent or original request
    ModelOverride string           // empty = resolve from Agent or Config
}
```

### Node kinds

| Kind | Behaviour |
|------|-----------|
| `NodeKindRoute` | Calls model once, captures string output, leaves `pctx.Messages` untouched |
| `NodeKindChain` | Runs a tool loop internally; commits only the final stripped response to `pctx.Messages` |
| `NodeKindScatter` | Fans out to N parallel branches via `BranchFactory`; merges results back with `Merge` |
| `NodeKindRespond` | Rebuilds request from `pctx`, proxies to vLLM, streams response to the HTTP client |

### Edge

```go
type Edge struct {
    From      string
    To        string
    Condition func(output string) bool  // nil = always taken
    Transform func(*PipelineCtx)        // mutate pctx before entering the next node
}
```

Edges are evaluated against `pctx.NodeOutputs[from]` after a node completes. The first matching edge is taken. Transforms run on the winning edge before the next node starts.

### Graph

```go
type Graph struct {
    Nodes   map[string]*Node
    Scatter map[string]ScatterConfig  // only for NodeKindScatter nodes
    Edges   []Edge
    Entry   string
    cfg     Config
}
```

### Branch (for Scatter)

```go
type Branch struct {
    Ctx      *PipelineCtx  // independent copy of state for this branch
    SubGraph *Graph         // the sub-graph this branch runs
}

type ScatterConfig struct {
    BranchFactory func(pctx *PipelineCtx) []Branch
    Merge         func(parent *PipelineCtx, branches []*PipelineCtx)
}
```

`BranchFactory` is called at runtime with the current `pctx`. It reads prior node outputs (e.g. a JSON list of tasks produced by an orchestrator node) and returns N branches — each with its own `pctx` copy and its own sub-graph. Different branches can run different sub-graphs.

---

## Gemma4 Thinking Rules

From the [Gemma4 documentation](https://ai.google.dev/gemma/docs/capabilities/thinking#multi-turn_example_with_thought_stripping):

1. **Standard multi-turn:** Strip thinking from all prior assistant turns before the next model call.
2. **Tool call exception:** Within a single logical turn, if the model makes tool calls, do NOT strip thinking between those calls — the model needs its own reasoning visible when processing tool results.
3. **Turn commit:** When storing a completed turn to cross-turn history, only the final response text survives — all thinking from that turn is stripped.

### How these map to the framework

| Rule | Where enforced |
|------|----------------|
| Rule 1 | `pctx.Messages` always holds stripped history; `stripAllThinking` is applied on commit |
| Rule 2 | Chain executor never calls strip on its internal `workingMessages` during the loop |
| Rule 3 | `HistoryPolicy.StripThinkingOnCommit = true` strips the final assistant message before appending to `pctx.Messages` |

---

## Message History Flow

```
Inbound request
    │
    ▼
pctx.Messages = [user msg]   ← no thinking in initial history

Chain node (tool loop)
  workingMessages = copy of pctx.Messages

  iteration 1:
    call model → <think>...</think> + tool_call
    append to workingMessages (thinking preserved — rule 2)
    execute tool, append result
    continue

  iteration 2:
    call model → <think>...</think> + final response
    if StripThinkingOnCommit:
      strip thinking from final assistant message  ← rule 3
    append clean final message to pctx.Messages

pctx.Messages = [user msg, assistant: final text only]

Next turn:
  pctx.Messages already has no thinking → rule 1 satisfied automatically
```

For a Route node, `pctx.Messages` is passed as-is to the model (read-only). Nothing is appended.

---

## Example Graphs

### 1. Classify → Respond (current router equivalent)

```go
graph := &agent.Graph{
    Entry: "classify",
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

### 2. Tool-using Chain agent

```go
graph := &agent.Graph{
    Entry: "research",
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

### 3. Scatter-gather research

```go
graph := &agent.Graph{
    Entry: "orchestrate",
    Nodes: map[string]*agent.Node{
        "orchestrate": {ID: "orchestrate", Kind: agent.NodeKindChain, Agent: orchestratorAgent},
        "explore":     {ID: "explore", Kind: agent.NodeKindScatter},
        "synthesize":  {ID: "synthesize", Kind: agent.NodeKindChain, Agent: synthesizerAgent},
        "respond":     {ID: "respond", Kind: agent.NodeKindRespond},
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
                    Content: "Research results:\n" + strings.Join(results, "\n\n"),
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

---

## File Map

| File | Responsibility |
|------|----------------|
| `internal/agent/types.go` | All type definitions |
| `internal/agent/history.go` | `stripAllThinking`, `applyWindow`, `DeepCopy` |
| `internal/agent/llm.go` | vLLM HTTP client (`callModel`) |
| `internal/agent/executor.go` | `Graph.Run`, `runRoute`, `runChain`, `runScatter`, `runRespond` |
| `internal/agent/stream.go` | `proxyStream`, `copyResponseHeaders` |
| `internal/agent/metrics.go` | Prometheus metrics |
| `main.go` | HTTP server wired with default classify→respond graph |
