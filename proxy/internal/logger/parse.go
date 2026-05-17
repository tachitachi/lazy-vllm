package logger

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

type Message struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning,omitempty"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
}

type ToolCall struct {
	ID       string   `json:"id,omitempty"`
	Type     string   `json:"type,omitempty"`
	Function ToolFunc `json:"function"`
}

type ToolFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type OutputLog struct {
	Reasoning string     `json:"reasoning,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ExtractTools extracts the "tools" array from a request body (OpenAI or Anthropic format).
// Both APIs use the same top-level key "tools". Returns canonical (minified) JSON,
// or [] if no tools are present.
func ExtractTools(body []byte) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return []byte("null")
	}
	if v, ok := raw["tools"]; ok {
		var tools []json.RawMessage
		if err := json.Unmarshal(v, &tools); err != nil {
			return []byte("null")
		}
		// Canonicalize: re-marshal to remove whitespace/key-order differences.
		canonical, _ := json.Marshal(tools)
		return canonical
	}
	return []byte("null")
}

// ParseMessages extracts the messages array from an OpenAI-format request body.
func ParseMessages(body []byte) []Message {
	var req struct {
		Messages []Message `json:"messages"`
	}
	json.Unmarshal(body, &req) //nolint:errcheck // body validated upstream
	return req.Messages
}

// ParseAnthropicMessages converts an Anthropic-format request body into the
// OpenAI-compatible Message slice used by the log UI. Anthropic content blocks
// (tool_use, tool_result, thinking, text) are normalized into role/content/
// tool_calls/tool_call_id fields that the UI already knows how to render.
func ParseAnthropicMessages(body []byte) []Message {
	var req struct {
		System   json.RawMessage    `json:"system"`
		Messages []anthropicMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}

	var result []Message

	if sys := extractSystemText(req.System); sys != "" {
		result = append(result, Message{Role: "system", Content: marshalJSON(sys)})
	}

	for _, m := range req.Messages {
		result = append(result, convertAnthropicMessage(m)...)
	}
	return result
}

// ── Anthropic message types ───────────────────────────────────────────────────

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or []anthropicBlock
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`          // tool_use
	Name      string          `json:"name,omitempty"`        // tool_use
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
	Content   json.RawMessage `json:"content,omitempty"`     // tool_result
}

func convertAnthropicMessage(m anthropicMessage) []Message {
	// String content — no conversion needed.
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return []Message{{Role: m.Role, Content: marshalJSON(s)}}
	}

	var blocks []anthropicBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return []Message{{Role: m.Role, Content: m.Content}}
	}

	switch m.Role {
	case "user":
		return convertAnthropicUserBlocks(blocks)
	case "assistant":
		return []Message{convertAnthropicAssistantBlocks(blocks)}
	default:
		return []Message{{Role: m.Role, Content: m.Content}}
	}
}

// convertAnthropicUserBlocks splits a user message into zero or more role="tool"
// messages (one per tool_result block) followed by a role="user" message for any
// remaining text content.
func convertAnthropicUserBlocks(blocks []anthropicBlock) []Message {
	var msgs []Message
	var textParts strings.Builder

	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			// tool_result.content can be a string or []block; extract plain text.
			var content string
			if err := json.Unmarshal(b.Content, &content); err != nil {
				var subBlocks []anthropicBlock
				if err2 := json.Unmarshal(b.Content, &subBlocks); err2 == nil {
					for _, sb := range subBlocks {
						if sb.Type == "text" {
							content += sb.Text
						}
					}
				}
			}
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: b.ToolUseID,
				Content:    marshalJSON(content),
			})
		case "text":
			textParts.WriteString(b.Text)
		}
	}

	if textParts.Len() > 0 {
		msgs = append(msgs, Message{Role: "user", Content: marshalJSON(textParts.String())})
	}
	return msgs
}

// convertAnthropicAssistantBlocks merges text/thinking/tool_use blocks into a
// single OpenAI-compatible assistant message.
func convertAnthropicAssistantBlocks(blocks []anthropicBlock) Message {
	var content, reasoning strings.Builder
	var toolCalls []ToolCall

	for _, b := range blocks {
		switch b.Type {
		case "text":
			content.WriteString(b.Text)
		case "thinking":
			reasoning.WriteString(b.Thinking)
		case "tool_use":
			args := "{}"
			if len(b.Input) > 0 {
				args = string(b.Input)
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:       b.ID,
				Type:     "function",
				Function: ToolFunc{Name: b.Name, Arguments: args},
			})
		}
	}

	msg := Message{
		Role:             "assistant",
		Content:          marshalJSON(content.String()),
		ReasoningContent: reasoning.String(),
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = marshalJSON(toolCalls)
	}
	return msg
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func marshalJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// extractSystemText pulls the string value from an Anthropic system field,
// which may be a plain string or an array of text content blocks.
func extractSystemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []anthropicBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	return ""
}

// ParseOpenAIOutput parses an OpenAI-format response (streaming or non-streaming).
func ParseOpenAIOutput(body []byte, streaming bool) OutputLog {
	if streaming {
		return parseOpenAISSE(body)
	}
	return parseOpenAIJSON(body)
}

// ParseAnthropicOutput parses an Anthropic-format response (streaming or non-streaming).
func ParseAnthropicOutput(body []byte, streaming bool) OutputLog {
	if streaming {
		return parseAnthropicSSE(body)
	}
	return parseAnthropicJSON(body)
}

// ── OpenAI ────────────────────────────────────────────────────────────────────

func parseOpenAIJSON(data []byte) OutputLog {
	var resp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &resp); err != nil || len(resp.Choices) == 0 {
		return OutputLog{}
	}
	msg := resp.Choices[0].Message
	out := OutputLog{Content: msg.Content, Reasoning: msg.Reasoning}
	for _, tc := range msg.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: ToolFunc{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	return out
}

func parseOpenAISSE(data []byte) OutputLog {
	var content, reasoning strings.Builder
	accumTools := make(map[int]*ToolCall)

	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		payload := line[6:]
		if bytes.Equal(payload, []byte("[DONE]")) {
			break
		}
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
		if err := json.Unmarshal(payload, &chunk); err != nil || len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		content.WriteString(d.Content)
		reasoning.WriteString(d.Reasoning)
		for _, tc := range d.ToolCalls {
			if _, exists := accumTools[tc.Index]; !exists {
				accumTools[tc.Index] = &ToolCall{
					ID:       tc.ID,
					Type:     tc.Type,
					Function: ToolFunc{Name: tc.Function.Name},
				}
			}
			accumTools[tc.Index].Function.Arguments += tc.Function.Arguments
		}
	}

	return OutputLog{
		Content:   content.String(),
		Reasoning: reasoning.String(),
		ToolCalls: sortedToolCalls(accumTools),
	}
}

// ── Anthropic ─────────────────────────────────────────────────────────────────

func parseAnthropicJSON(data []byte) OutputLog {
	var resp struct {
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return OutputLog{}
	}
	var content, reasoning strings.Builder
	var toolCalls []ToolCall
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			content.WriteString(block.Text)
		case "thinking":
			reasoning.WriteString(block.Thinking)
		case "tool_use":
			args := "{}"
			if len(block.Input) > 0 {
				args = string(block.Input)
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:       block.ID,
				Type:     "function",
				Function: ToolFunc{Name: block.Name, Arguments: args},
			})
		}
	}
	return OutputLog{
		Content:   content.String(),
		Reasoning: reasoning.String(),
		ToolCalls: toolCalls,
	}
}

func parseAnthropicSSE(data []byte) OutputLog {
	var content, reasoning strings.Builder
	accumTools := make(map[int]*ToolCall)

	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		payload := line[6:]
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
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			continue
		}
		switch event.Type {
		case "content_block_start":
			if event.ContentBlock.Type == "tool_use" {
				idx := len(accumTools)
				accumTools[idx] = &ToolCall{
					ID:       event.ContentBlock.ID,
					Type:     "function",
					Function: ToolFunc{Name: event.ContentBlock.Name},
				}
			}
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				content.WriteString(event.Delta.Text)
			case "thinking_delta":
				reasoning.WriteString(event.Delta.Thinking)
			case "input_json_delta":
				idx := len(accumTools) - 1
				if idx >= 0 {
					if tc, ok := accumTools[idx]; ok {
						tc.Function.Arguments += event.Delta.PartialJSON
					}
				}
			}
		}
	}

	return OutputLog{
		Content:   content.String(),
		Reasoning: reasoning.String(),
		ToolCalls: sortedToolCalls(accumTools),
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func sortedToolCalls(m map[int]*ToolCall) []ToolCall {
	if len(m) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(m))
	for idx := range m {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	out := make([]ToolCall, 0, len(indexes))
	for _, idx := range indexes {
		out = append(out, *m[idx])
	}
	return out
}
