package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
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
	if reqLog := ReqLogFromCtx(ctx); reqLog != nil {
		reqLog.AddCall(node.ID, "route", 0, msgs, resp)
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
	iteration := 0

	for {
		windowed := applyWindow(workingMessages, window)
		resp, err := callModel(ctx, g.Cfg, node.Agent, pctx, windowed)
		if err != nil {
			return "", err
		}
		if reqLog := ReqLogFromCtx(ctx); reqLog != nil {
			reqLog.AddCall(node.ID, "chain", iteration, windowed, resp)
		}
		iteration++

		if len(resp.ToolCalls) > 0 {
			// Tool call turn: preserve thinking in workingMessages (Gemma4 rule 2).
			assistantMsg := ChatMessage{
				Role:             "assistant",
				Content:          resp.Content,
				ReasoningContent: resp.Reasoning,
				ToolCalls:        resp.ToolCalls,
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
			Role:             "assistant",
			Content:          resp.Content,
			ReasoningContent: resp.Reasoning,
		}
		if node.Agent.History.StripThinkingOnCommit {
			finalMsg = StripMessageThinking(finalMsg)
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
	var body []byte
	var endpoint string
	var passthroughHeaders []string

	if pctx.Format == FormatAnthropic {
		var err error
		body, err = BuildAnthropicRequestBody(pctx.OriginalBody, pctx.Messages, pctx)
		if err != nil {
			http.Error(w, "failed to rebuild request", http.StatusInternalServerError)
			return fmt.Errorf("respond: rebuild anthropic: %w", err)
		}
		endpoint = g.Cfg.VLLMBaseURL + "/v1/messages"
		passthroughHeaders = []string{"x-api-key", "anthropic-version", "anthropic-beta", "Authorization"}
	} else {
		var err error
		body, err = g.rebuildRequestBody(pctx)
		if err != nil {
			http.Error(w, "failed to rebuild request", http.StatusInternalServerError)
			return fmt.Errorf("respond: rebuild openai: %w", err)
		}
		endpoint = g.Cfg.VLLMBaseURL + "/v1/chat/completions"
		passthroughHeaders = []string{"Authorization", "x-api-key"}
	}

	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "failed to build upstream request", http.StatusInternalServerError)
		return fmt.Errorf("respond: build request: %w", err)
	}
	upstream.Header.Set("Content-Type", "application/json")
	for _, h := range passthroughHeaders {
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

	reqLog := ReqLogFromCtx(ctx)
	inputMsgs := pctx.Messages

	if pctx.Stream {
		r := io.Reader(resp.Body)
		if slog.Default().Enabled(ctx, slog.LevelDebug) || reqLog != nil {
			sl := &streamLogger{r: resp.Body, format: pctx.Format}
			if reqLog != nil {
				sl.reqLog = reqLog
				sl.nodeID = node.ID
				sl.inputMsgs = inputMsgs
				sl.accumTools = make(map[int]*ToolCall)
			}
			r = sl
		}
		proxyStream(w, r)
	} else {
		body, _ := io.ReadAll(resp.Body)
		if slog.Default().Enabled(ctx, slog.LevelDebug) {
			logNonStreamResponse(body, pctx.Format)
		}
		if reqLog != nil {
			if mr := parseNonStreamResponse(body, pctx.Format); mr != nil {
				reqLog.AddCall(node.ID, "respond", 0, inputMsgs, mr)
			}
		}
		w.Write(body)
	}
	return nil
}

// streamLogger wraps an io.Reader and logs each SSE chunk as it passes through.
// It buffers bytes until a newline, then parses and logs the completed line.
// When reqLog is set, it also accumulates the full response for disk logging.
type streamLogger struct {
	r          io.Reader
	format     APIFormat
	lineBuf    []byte
	responseID string // captured from first chunk; used to correlate interleaved responses

	reqLog    *RequestLog
	nodeID    string
	inputMsgs []ChatMessage

	accumContent   strings.Builder
	accumReasoning strings.Builder
	accumTools     map[int]*ToolCall // index → partial tool call being built
}

func newStreamLogger(r io.Reader, format APIFormat) io.Reader {
	return &streamLogger{r: r, format: format}
}

func (s *streamLogger) Read(p []byte) (n int, err error) {
	n, err = s.r.Read(p)
	for _, b := range p[:n] {
		if b == '\n' {
			s.processLine(bytes.TrimSpace(s.lineBuf))
			s.lineBuf = s.lineBuf[:0]
		} else {
			s.lineBuf = append(s.lineBuf, b)
		}
	}
	if err != nil {
		if len(s.lineBuf) > 0 {
			s.processLine(bytes.TrimSpace(s.lineBuf))
			s.lineBuf = s.lineBuf[:0]
		}
		s.flushToLog()
	}
	return
}

func (s *streamLogger) processLine(line []byte) {
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return
	}
	line = bytes.TrimPrefix(line, []byte("data: "))
	if len(line) == 0 || string(line) == "[DONE]" {
		return
	}
	if s.format == FormatAnthropic {
		s.logAnthropicChunk(line)
		if s.reqLog != nil {
			s.accumAnthropicChunk(line)
		}
	} else {
		s.logOpenAIChunk(line)
		if s.reqLog != nil {
			s.accumOpenAIChunk(line)
		}
	}
}

func (s *streamLogger) accumOpenAIChunk(data []byte) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil || len(chunk.Choices) == 0 {
		return
	}
	d := chunk.Choices[0].Delta
	s.accumContent.WriteString(d.Content)
	s.accumReasoning.WriteString(d.Reasoning)
	for _, tc := range d.ToolCalls {
		if _, exists := s.accumTools[tc.Index]; !exists {
			s.accumTools[tc.Index] = &ToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: ToolFunction{Name: tc.Function.Name},
			}
		}
		s.accumTools[tc.Index].Function.Arguments += tc.Function.Arguments
	}
}

func (s *streamLogger) accumAnthropicChunk(data []byte) {
	var event struct {
		Type  string `json:"type"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
		ContentBlock struct {
			Type string `json:"type"`
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"content_block"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return
	}
	switch event.Type {
	case "content_block_start":
		if event.ContentBlock.Type == "tool_use" {
			idx := len(s.accumTools)
			s.accumTools[idx] = &ToolCall{
				ID:   event.ContentBlock.ID,
				Type: "function",
				Function: ToolFunction{Name: event.ContentBlock.Name},
			}
		}
	case "content_block_delta":
		switch event.Delta.Type {
		case "text_delta":
			s.accumContent.WriteString(event.Delta.Text)
		case "thinking_delta":
			s.accumReasoning.WriteString(event.Delta.Thinking)
		case "input_json_delta":
			idx := len(s.accumTools) - 1
			if idx >= 0 {
				if tc, ok := s.accumTools[idx]; ok {
					tc.Function.Arguments += event.Delta.PartialJSON
				}
			}
		}
	}
}

func (s *streamLogger) flushToLog() {
	if s.reqLog == nil {
		return
	}
	indexes := make([]int, 0, len(s.accumTools))
	for idx := range s.accumTools {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	toolCalls := make([]ToolCall, 0, len(indexes))
	for _, idx := range indexes {
		toolCalls = append(toolCalls, *s.accumTools[idx])
	}
	mr := &modelResponse{
		Content:   s.accumContent.String(),
		Reasoning: s.accumReasoning.String(),
		ToolCalls: toolCalls,
	}
	s.reqLog.AddCall(s.nodeID, "respond", 0, s.inputMsgs, mr)
}

func parseNonStreamResponse(body []byte, format APIFormat) *modelResponse {
	if format == FormatAnthropic {
		return parseAnthropicNonStreamResponse(body)
	}
	return parseOpenAINonStreamResponse(body)
}

func parseOpenAINonStreamResponse(body []byte) *modelResponse {
	var result struct {
		Choices []struct {
			Message struct {
				Content   any        `json:"content"`
				Reasoning string     `json:"reasoning"`
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Choices) == 0 {
		return nil
	}
	msg := result.Choices[0].Message
	return &modelResponse{
		Content:   extractTextContent(msg.Content),
		Reasoning: msg.Reasoning,
		ToolCalls: msg.ToolCalls,
	}
}

func parseAnthropicNonStreamResponse(body []byte) *modelResponse {
	var result struct {
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}
	var content, reasoning string
	var toolCalls []ToolCall
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			content += block.Text
		case "thinking":
			reasoning += block.Thinking
		case "tool_use":
			args := "{}"
			if len(block.Input) > 0 {
				args = string(block.Input)
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: ToolFunction{
					Name:      block.Name,
					Arguments: args,
				},
			})
		}
	}
	return &modelResponse{Content: content, Reasoning: reasoning, ToolCalls: toolCalls}
}

func (s *streamLogger) logOpenAIChunk(data []byte) {
	var chunk struct {
		ID      string `json:"id"`
		Choices []struct {
			Delta struct {
				Content   string     `json:"content"`
				Reasoning string     `json:"reasoning"`
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil || len(chunk.Choices) == 0 {
		return
	}
	if s.responseID == "" {
		s.responseID = chunk.ID
	}
	choice := chunk.Choices[0]
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		slog.Debug("stream finish", "id", s.responseID, "finish_reason", *choice.FinishReason)
	}
	delta := choice.Delta
	if delta.Content == "" && delta.Reasoning == "" && len(delta.ToolCalls) == 0 {
		return
	}
	slog.Debug("stream chunk",
		"id", s.responseID,
		"content", delta.Content,
		"reasoning", delta.Reasoning,
		"tool_calls", len(delta.ToolCalls),
	)
}

func (s *streamLogger) logAnthropicChunk(data []byte) {
	var event struct {
		Type    string `json:"type"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
		ContentBlock struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content_block"`
		Delta struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			Thinking   string `json:"thinking"`
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return
	}
	if event.Type == "message_start" && event.Message.ID != "" {
		s.responseID = event.Message.ID
		return
	}
	switch event.Type {
	case "message_delta":
		if event.Delta.StopReason != "" {
			slog.Debug("stream finish", "id", s.responseID, "stop_reason", event.Delta.StopReason)
		}
	case "content_block_start":
		if event.ContentBlock.Type == "tool_use" {
			slog.Debug("stream chunk", "id", s.responseID, "tool_call", event.ContentBlock.Name)
		}
	case "content_block_delta":
		switch event.Delta.Type {
		case "text_delta":
			if event.Delta.Text != "" {
				slog.Debug("stream chunk", "id", s.responseID, "content", event.Delta.Text)
			}
		case "thinking_delta":
			if event.Delta.Thinking != "" {
				slog.Debug("stream chunk", "id", s.responseID, "reasoning", event.Delta.Thinking)
			}
		}
	}
}

func logNonStreamResponse(body []byte, format APIFormat) {
	if format == FormatAnthropic {
		logAnthropicNonStreamResponse(body)
	} else {
		logOpenAINonStreamResponse(body)
	}
}

func logOpenAINonStreamResponse(body []byte) {
	var result struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content   any        `json:"content"`
				Reasoning string     `json:"reasoning"`
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Choices) == 0 {
		return
	}
	msg := result.Choices[0].Message
	slog.Debug("respond output",
		"id", result.ID,
		"content", extractTextContent(msg.Content),
		"reasoning", msg.Reasoning,
		"tool_calls", len(msg.ToolCalls),
	)
}

func logAnthropicNonStreamResponse(body []byte) {
	var result struct {
		ID      string `json:"id"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return
	}
	var content, reasoning string
	toolCalls := 0
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			content += block.Text
		case "thinking":
			reasoning += block.Thinking
		case "tool_use":
			toolCalls++
		}
	}
	slog.Debug("respond output",
		"id", result.ID,
		"content", content,
		"reasoning", reasoning,
		"tool_calls", toolCalls,
	)
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
