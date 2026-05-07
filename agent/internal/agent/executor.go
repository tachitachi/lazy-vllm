package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/sync/errgroup"
)

// Run executes the graph for a single request. It walks nodes following edges
// until it reaches a NodeKindRespond node, which writes the HTTP response.
func (g *Graph) Run(ctx context.Context, pctx *PipelineCtx, w http.ResponseWriter) error {
	nodeID := g.Entry
	for {
		node, ok := g.Nodes[nodeID]
		if !ok {
			return fmt.Errorf("graph: unknown node %q", nodeID)
		}

		var output string

		switch node.Kind {
		case NodeKindRoute:
			var err error
			output, err = g.runRoute(ctx, node, pctx)
			if err != nil {
				return fmt.Errorf("route %q: %w", nodeID, err)
			}

		case NodeKindChain:
			var err error
			output, err = g.runChain(ctx, node, pctx)
			if err != nil {
				return fmt.Errorf("chain %q: %w", nodeID, err)
			}

		case NodeKindScatter:
			if err := g.runScatter(ctx, node, pctx); err != nil {
				return fmt.Errorf("scatter %q: %w", nodeID, err)
			}
			output = "" // scatter nodes have no string output; edges should use nil Condition

		case NodeKindRespond:
			return g.runRespond(ctx, node, pctx, w)
		}

		pctx.NodeOutputs[nodeID] = output
		slog.Debug("node complete", "node", nodeID, "kind", node.Kind, "output", output)

		edge := g.findEdge(nodeID, output)
		if edge == nil {
			return fmt.Errorf("graph: no edge from %q matched output %q", nodeID, output)
		}
		if edge.Transform != nil {
			edge.Transform(pctx)
		}
		nodeID = edge.To
	}
}

func (g *Graph) findEdge(from, output string) *Edge {
	for i := range g.Edges {
		e := &g.Edges[i]
		if e.From != from {
			continue
		}
		if e.Condition == nil || e.Condition(output) {
			return e
		}
	}
	return nil
}

// runRoute calls the model once, captures the string output, leaves pctx.Messages untouched.
func (g *Graph) runRoute(ctx context.Context, node *Node, pctx *PipelineCtx) (string, error) {
	window := g.windowSize(node.Agent)
	msgs := applyWindow(pctx.Messages, window)

	resp, err := callModel(ctx, g.Cfg, node.Agent, pctx, msgs)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.ToUpper(resp.Content)), nil
}

// runChain runs the agent's tool loop. It maintains an internal workingMessages
// slice where thinking is preserved throughout the loop (Gemma4 rule 2).
// On completion, only the final assistant message (thinking stripped if configured)
// is committed to pctx.Messages (Gemma4 rules 1 & 3).
func (g *Graph) runChain(ctx context.Context, node *Node, pctx *PipelineCtx) (string, error) {
	workingMessages := make([]ChatMessage, len(pctx.Messages))
	copy(workingMessages, pctx.Messages)

	window := g.windowSize(node.Agent)

	for {
		windowed := applyWindow(workingMessages, window)
		resp, err := callModel(ctx, g.Cfg, node.Agent, pctx, windowed)
		if err != nil {
			return "", err
		}

		if len(resp.ToolCalls) > 0 {
			// Tool call turn: preserve thinking in workingMessages (Gemma4 rule 2).
			assistantMsg := ChatMessage{
				Role:      "assistant",
				Content:   resp.Content,
				ToolCalls: resp.ToolCalls,
			}
			workingMessages = append(workingMessages, assistantMsg)

			for _, tc := range resp.ToolCalls {
				result, err := g.executeTool(ctx, node.Agent, tc)
				if err != nil {
					slog.Warn("tool execution failed", "tool", tc.Function.Name, "err", err)
					result = fmt.Sprintf("error: %v", err)
				}
				slog.Debug("tool result", "tool", tc.Function.Name, "result", result)
				workingMessages = append(workingMessages, ChatMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    result,
				})
			}
			continue
		}

		// Final turn: commit to pctx.Messages.
		finalMsg := ChatMessage{
			Role:    "assistant",
			Content: resp.Content,
		}
		if node.Agent.History.StripThinkingOnCommit {
			finalMsg = stripMessageThinking(finalMsg)
		}
		pctx.Messages = append(pctx.Messages, finalMsg)

		text := extractTextContent(finalMsg.Content)
		return text, nil
	}
}

func (g *Graph) executeTool(ctx context.Context, ag Agent, tc ToolCall) (string, error) {
	for _, def := range ag.Tools {
		if def.Name != tc.Function.Name {
			continue
		}
		return def.Handler(ctx, json.RawMessage(tc.Function.Arguments))
	}
	return "", fmt.Errorf("unknown tool %q", tc.Function.Name)
}

// runScatter fans out to N branches in parallel, waits for all to complete,
// then calls Merge to fold results back into pctx.
func (g *Graph) runScatter(ctx context.Context, node *Node, pctx *PipelineCtx) error {
	sc, ok := g.Scatter[node.ID]
	if !ok {
		return fmt.Errorf("scatter: no ScatterConfig for node %q", node.ID)
	}

	branches := sc.BranchFactory(pctx)
	if len(branches) == 0 {
		return nil
	}

	completedCtxs := make([]*PipelineCtx, len(branches))
	eg, egCtx := errgroup.WithContext(ctx)

	for i, b := range branches {
		i, b := i, b // capture
		eg.Go(func() error {
			// Scatter branches terminate when their sub-graph has no more edges,
			// or when they reach their own NodeKindRespond (which shouldn't write
			// to the parent's ResponseWriter — scatter branches use a no-op writer).
			nopWriter := &nopResponseWriter{}
			if err := b.SubGraph.Run(egCtx, b.Ctx, nopWriter); err != nil {
				return fmt.Errorf("branch %d: %w", i, err)
			}
			completedCtxs[i] = b.Ctx
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	sc.Merge(pctx, completedCtxs)
	return nil
}

// runRespond rebuilds the request from pctx state and proxies it to vLLM,
// streaming the response back to the client.
func (g *Graph) runRespond(ctx context.Context, node *Node, pctx *PipelineCtx, w http.ResponseWriter) error {
	body, err := g.rebuildRequestBody(pctx)
	if err != nil {
		http.Error(w, "failed to rebuild request", http.StatusInternalServerError)
		return fmt.Errorf("respond: rebuild: %w", err)
	}

	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost,
		g.Cfg.VLLMBaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		http.Error(w, "failed to build upstream request", http.StatusInternalServerError)
		return fmt.Errorf("respond: build request: %w", err)
	}
	upstream.Header.Set("Content-Type", "application/json")
	for _, h := range []string{"Authorization", "x-api-key"} {
		if v := pctx.OriginalHeaders.Get(h); v != "" {
			upstream.Header.Set(h, v)
		}
	}

	resp, err := http.DefaultClient.Do(upstream)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return fmt.Errorf("respond: upstream: %w", err)
	}
	defer resp.Body.Close()

	copyResponseHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)

	if pctx.Stream {
		proxyStream(w, resp.Body)
	} else {
		io.Copy(w, resp.Body)
	}
	return nil
}

// rebuildRequestBody takes the original request body, replaces "messages" with
// pctx.Messages, and applies any overrides (ThinkingMode, ModelOverride).
func (g *Graph) rebuildRequestBody(pctx *PipelineCtx) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(pctx.OriginalBody, &top); err != nil {
		return nil, err
	}

	msgs, err := json.Marshal(pctx.Messages)
	if err != nil {
		return nil, err
	}
	top["messages"] = msgs

	if pctx.ThinkingMode != nil {
		kwargs, err := json.Marshal(map[string]bool{"enable_thinking": *pctx.ThinkingMode})
		if err != nil {
			return nil, err
		}
		top["chat_template_kwargs"] = kwargs
	}

	if pctx.ModelOverride != "" {
		model, err := json.Marshal(pctx.ModelOverride)
		if err != nil {
			return nil, err
		}
		top["model"] = model
	}

	return json.Marshal(top)
}

func (g *Graph) windowSize(ag Agent) int {
	if ag.History.WindowSize > 0 {
		return ag.History.WindowSize
	}
	return g.Cfg.WindowSize
}

// nopResponseWriter discards writes from scatter branch sub-graphs that
// reach a NodeKindRespond node. Branches should not write to the parent's client.
type nopResponseWriter struct {
	header http.Header
	status int
}

func (n *nopResponseWriter) Header() http.Header {
	if n.header == nil {
		n.header = make(http.Header)
	}
	return n.header
}
func (n *nopResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (n *nopResponseWriter) WriteHeader(code int)        { n.status = code }
