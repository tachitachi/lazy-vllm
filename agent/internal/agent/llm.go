package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type modelResponse struct {
	Content   string     // final text content
	Reasoning string     // thinking content from reasoning_content field
	ToolCalls []ToolCall // non-empty means the model wants to call tools
}

type toolSchema struct {
	Type     string          `json:"type"`
	Function toolFunctionDef `json:"function"`
}

type toolFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// callModel sends msgs to vLLM and returns the model's response.
// ThinkingMode and Model are resolved from a (agent → pctx → default) priority chain.
func callModel(ctx context.Context, cfg Config, ag Agent, pctx *PipelineCtx, msgs []ChatMessage) (*modelResponse, error) {
	model := resolveModel(cfg, ag, pctx)
	thinking := resolveThinking(ag, pctx)

	payload := map[string]any{
		"model":                model,
		"messages":             buildPayloadMessages(ag.SystemPrompt, msgs),
		"stream":               false,
		"chat_template_kwargs": map[string]bool{"enable_thinking": thinking},
	}

	if ag.MaxTokens > 0 {
		payload["max_tokens"] = ag.MaxTokens
	}
	if len(ag.StructuredChoice) > 0 {
		payload["structured_outputs"] = map[string]any{"choice": ag.StructuredChoice}
	}
	if len(ag.Tools) > 0 {
		schemas := make([]toolSchema, len(ag.Tools))
		for i, t := range ag.Tools {
			schemas[i] = toolSchema{
				Type: "function",
				Function: toolFunctionDef{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			}
		}
		payload["tools"] = schemas
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("callModel marshal: %w", err)
	}

	if pctx.BackendURL == "" {
		return nil, fmt.Errorf("callModel: no backend URL set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		pctx.BackendURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("callModel request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key := pctx.OriginalHeaders.Get("x-api-key"); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else if auth := pctx.OriginalHeaders.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("callModel http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("callModel read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("callModel status %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content          any        `json:"content"`
				ReasoningContent string     `json:"reasoning"`
				ToolCalls        []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("callModel unmarshal: %w", err)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("callModel: no choices in response")
	}

	choice := result.Choices[0].Message
	content := extractTextContent(choice.Content)

	return &modelResponse{
		Content:   content,
		Reasoning: choice.ReasoningContent,
		ToolCalls: choice.ToolCalls,
	}, nil
}

func buildPayloadMessages(systemPrompt string, msgs []ChatMessage) []ChatMessage {
	if systemPrompt == "" {
		return msgs
	}
	out := make([]ChatMessage, 0, len(msgs)+1)
	out = append(out, ChatMessage{Role: "system", Content: systemPrompt})
	out = append(out, msgs...)
	return out
}

func extractTextContent(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []any:
		var parts []string
		for _, item := range val {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return joinNonEmpty(parts)
	}
	return ""
}

func joinNonEmpty(ss []string) string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	result := ""
	for i, s := range out {
		if i > 0 {
			result += " "
		}
		result += s
	}
	return result
}

func resolveModel(cfg Config, ag Agent, pctx *PipelineCtx) string {
	if pctx.ModelOverride != "" {
		return pctx.ModelOverride
	}
	if ag.Model != "" {
		return ag.Model
	}
	return cfg.ModelName
}

func resolveThinking(ag Agent, pctx *PipelineCtx) bool {
	if ag.ThinkingMode != nil {
		return *ag.ThinkingMode
	}
	if pctx.ThinkingMode != nil {
		return *pctx.ThinkingMode
	}
	return true // fail-open: match existing router behaviour
}
