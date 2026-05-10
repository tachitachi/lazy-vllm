package agent

import (
	"context"
	"encoding/json"
	"net/http"
)

type BackendRule struct {
	Prefix string
	URL    string
}

type Config struct {
	Backends   []BackendRule
	Port       int
	WindowSize int
	ModelName  string
}

type HistoryPolicy struct {
	StripThinkingOnCommit bool
	WindowSize            int // 0 = no limit; overrides Config.WindowSize for this agent
}

type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Handler     func(ctx context.Context, input json.RawMessage) (string, error)
}

type Agent struct {
	SystemPrompt     string
	Model            string // empty = inherit from Config
	MaxTokens        int
	ThinkingMode     *bool    // nil = inherit from pctx; true/false = force
	StructuredChoice []string // enables structured_output.choice
	Tools            []ToolDef
	History          HistoryPolicy
}

type NodeKind int

const (
	NodeKindRoute   NodeKind = iota // call once, capture output string, pctx.Messages untouched
	NodeKindChain                   // tool loop, commits final response to pctx.Messages
	NodeKindScatter                 // fan-out to N branches in parallel, merge back
	NodeKindRespond                 // rebuild request from pctx, proxy to vLLM, stream to client
)

type Node struct {
	ID    string
	Agent Agent
	Kind  NodeKind
}

type Branch struct {
	Ctx      *PipelineCtx
	SubGraph *Graph
}

type ScatterConfig struct {
	BranchFactory func(pctx *PipelineCtx) []Branch
	Merge         func(parent *PipelineCtx, branches []*PipelineCtx)
}

type Edge struct {
	From      string
	To        string
	Condition func(output string) bool // nil = always taken
	Transform func(*PipelineCtx)       // mutate pctx before entering the next node
}

type Graph struct {
	Nodes   map[string]*Node
	Scatter map[string]ScatterConfig // keyed by node ID
	Edges   []Edge
	Entry   string
	Cfg     Config
}

type APIFormat int

const (
	FormatOpenAI APIFormat = iota
	FormatAnthropic
)

type PipelineCtx struct {
	OriginalBody    []byte
	OriginalHeaders http.Header
	Stream          bool
	Format          APIFormat // FormatOpenAI or FormatAnthropic

	Messages    []ChatMessage     // cross-turn history; thinking stripped from all prior turns
	NodeOutputs map[string]string // captured outputs keyed by node ID

	ThinkingMode  *bool  // nil = resolve from Agent then original request
	ModelOverride string // empty = resolve from Agent then Config
	BackendURL    string // resolved upstream URL for this request
}

type ChatMessage struct {
	Role             string     `json:"role"`
	Content          any        `json:"content"`             // string or []ContentBlock
	ReasoningContent string     `json:"reasoning,omitempty"` // vLLM OpenAI format thinking
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
