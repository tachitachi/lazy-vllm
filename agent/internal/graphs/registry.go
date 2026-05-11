package graphs

import "agent-graph/internal/agent"

// Registry maps agent names to graph factory functions.
var Registry = map[string]func(cfg agent.Config) *agent.Graph{
	"thinking-router": buildThinkingRouterGraph,
	"assistant":       buildAssistantGraph,
}

// Lookup returns the graph factory for the named agent, or nil if unknown.
func Lookup(name string) func(cfg agent.Config) *agent.Graph {
	return Registry[name]
}

// Names returns a sorted list of registered agent names.
func Names() []string {
	names := make([]string, 0, len(Registry))
	for k := range Registry {
		names = append(names, k)
	}
	return names
}

const routingPrompt = "You are a query router. Classify the user's latest message as DIRECT or REASONING based on the cognitive depth required to provide an accurate response.\n\n" +
	"DIRECT: Use for shallow-processing tasks. This includes simple factual retrieval, greetings, or trivial transformations where the answer is immediate and requires no internal logical steps, synthesis of information, or complex reasoning.\n\n" +
	"REASONING: Use for deep-processing tasks. This includes queries that require:\n" +
	"- Multi-step logical deduction or sequential processing.\n" +
	"- Synthesis or abstraction (e.g., summarizing, identifying themes, or distilling information).\n" +
	"- Analytical reasoning (e.g., comparisons, critiques, or explaining 'why').\n" +
	"- Complex constraint satisfaction (e.g., following intricate formatting or stylistic rules).\n" +
	"- Processing high-density or structurally complex input.\n\n" +
	"Classify the message based on the required depth of processing."

func boolPtr(v bool) *bool { return &v }

// buildThinkingRouterGraph classifies the prompt as DIRECT or REASONING and
// enables thinking mode accordingly before forwarding to the model.
func buildThinkingRouterGraph(cfg agent.Config) *agent.Graph {
	return &agent.Graph{
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
			"respond": {
				ID:   "respond",
				Kind: agent.NodeKindChain,
				Agent: agent.Agent{
					History: agent.HistoryPolicy{StripThinkingOnCommit: true},
				},
			},
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
}

// buildAssistantGraph is a simple single-turn assistant with no routing.
func buildAssistantGraph(cfg agent.Config) *agent.Graph {
	return &agent.Graph{
		Entry: "respond",
		Cfg:   cfg,
		Nodes: map[string]*agent.Node{
			"respond": {
				ID:   "respond",
				Kind: agent.NodeKindChain,
				Agent: agent.Agent{
					SystemPrompt: "You are a helpful assistant.",
					History:      agent.HistoryPolicy{StripThinkingOnCommit: true},
				},
			},
		},
		Edges: []agent.Edge{},
	}
}
