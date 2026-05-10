package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AnthropicContentBlock represents a single content block in an Anthropic message.
type AnthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`          // tool_use
	Name      string          `json:"name,omitempty"`        // tool_use
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
	Content   any             `json:"content,omitempty"`     // tool_result
}

// AnthropicMessage is the wire format for a message in the Anthropic API.
type AnthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []AnthropicContentBlock
}

// AnthropicToChatMessages converts an Anthropic messages request into the
// internal []ChatMessage format (OpenAI-compatible) used by the graph.
// The system prompt is prepended as a role="system" message.
func AnthropicToChatMessages(system any, messages []AnthropicMessage) []ChatMessage {
	var result []ChatMessage

	if sys := extractAnthropicText(system); sys != "" {
		result = append(result, ChatMessage{Role: "system", Content: sys})
	}

	for _, m := range messages {
		switch m.Role {
		case "user":
			result = append(result, convertAnthropicUserMsg(m)...)
		case "assistant":
			result = append(result, convertAnthropicAssistantMsg(m))
		default:
			result = append(result, ChatMessage{Role: m.Role, Content: extractAnthropicText(m.Content)})
		}
	}
	return result
}

// convertAnthropicUserMsg converts an Anthropic user message to []ChatMessage.
// A user message may contain tool_result blocks (one per prior tool call), which
// become individual role="tool" messages in OpenAI format.
func convertAnthropicUserMsg(m AnthropicMessage) []ChatMessage {
	blocks, ok := decodeAnthropicBlocks(m.Content)
	if !ok {
		return []ChatMessage{{Role: "user", Content: extractAnthropicText(m.Content)}}
	}

	var toolResults []AnthropicContentBlock
	var otherBlocks []AnthropicContentBlock
	for _, b := range blocks {
		if b.Type == "tool_result" {
			toolResults = append(toolResults, b)
		} else {
			otherBlocks = append(otherBlocks, b)
		}
	}

	var msgs []ChatMessage
	for _, tr := range toolResults {
		msgs = append(msgs, ChatMessage{
			Role:       "tool",
			ToolCallID: tr.ToolUseID,
			Content:    extractAnthropicText(tr.Content),
		})
	}
	if len(otherBlocks) > 0 {
		msgs = append(msgs, ChatMessage{Role: "user", Content: otherBlocks})
	}
	return msgs
}

// convertAnthropicAssistantMsg converts an Anthropic assistant message to ChatMessage.
// tool_use blocks become the ToolCalls field; remaining blocks (text, thinking) stay as Content.
func convertAnthropicAssistantMsg(m AnthropicMessage) ChatMessage {
	blocks, ok := decodeAnthropicBlocks(m.Content)
	if !ok {
		return ChatMessage{Role: "assistant", Content: extractAnthropicText(m.Content)}
	}

	var toolCalls []ToolCall
	var remaining []AnthropicContentBlock
	for _, b := range blocks {
		if b.Type == "tool_use" {
			input := b.Input
			if !json.Valid(input) {
				input = json.RawMessage(`{}`)
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:   b.ID,
				Type: "function",
				Function: ToolFunction{
					Name:      b.Name,
					Arguments: string(input),
				},
			})
		} else {
			remaining = append(remaining, b)
		}
	}

	msg := ChatMessage{Role: "assistant"}
	if len(remaining) > 0 {
		msg.Content = remaining
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	return msg
}

// BuildAnthropicRequestBody takes the original Anthropic request body, replaces
// system + messages with values from pctx.Messages, and applies any overrides.
func BuildAnthropicRequestBody(originalBody []byte, msgs []ChatMessage, pctx *PipelineCtx) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(originalBody, &top); err != nil {
		return nil, fmt.Errorf("BuildAnthropicRequestBody: %w", err)
	}

	system, anthropicMsgs := chatMessagesToAnthropic(msgs)

	if system != "" {
		sysJSON, err := json.Marshal(system)
		if err != nil {
			return nil, err
		}
		top["system"] = sysJSON
	} else {
		delete(top, "system")
	}

	msgsJSON, err := json.Marshal(anthropicMsgs)
	if err != nil {
		return nil, err
	}
	top["messages"] = msgsJSON

	if pctx.ThinkingMode != nil {
		kwargs, err := json.Marshal(map[string]bool{"enable_thinking": *pctx.ThinkingMode})
		if err != nil {
			return nil, err
		}
		top["chat_template_kwargs"] = kwargs
	}

	if pctx.ModelOverride != "" {
		modelJSON, err := json.Marshal(pctx.ModelOverride)
		if err != nil {
			return nil, err
		}
		top["model"] = modelJSON
	}

	return json.Marshal(top)
}

// chatMessagesToAnthropic converts internal []ChatMessage back to Anthropic format.
// Returns the system string and the messages array separately.
// Consecutive role="tool" messages are grouped into a single Anthropic user message
// (Anthropic requires tool results to be in a single user turn).
func chatMessagesToAnthropic(msgs []ChatMessage) (string, []AnthropicMessage) {
	var systemParts []string
	var out []AnthropicMessage

	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case "system":
			systemParts = append(systemParts, extractTextContent(m.Content))

		case "user":
			out = append(out, AnthropicMessage{
				Role:    "user",
				Content: internalContentToAnthropic(m.Content),
			})

		case "tool":
			// Collect consecutive tool messages into one Anthropic user message.
			blocks := []AnthropicContentBlock{toolMsgToResultBlock(m)}
			for i+1 < len(msgs) && msgs[i+1].Role == "tool" {
				i++
				blocks = append(blocks, toolMsgToResultBlock(msgs[i]))
			}
			out = append(out, AnthropicMessage{Role: "user", Content: blocks})

		case "assistant":
			out = append(out, AnthropicMessage{
				Role:    "assistant",
				Content: assistantToAnthropicBlocks(m),
			})
		}
	}

	return strings.Join(systemParts, "\n"), out
}

func toolMsgToResultBlock(m ChatMessage) AnthropicContentBlock {
	return AnthropicContentBlock{
		Type:      "tool_result",
		ToolUseID: m.ToolCallID,
		Content:   extractTextContent(m.Content),
	}
}

func assistantToAnthropicBlocks(m ChatMessage) []AnthropicContentBlock {
	var blocks []AnthropicContentBlock

	switch v := m.Content.(type) {
	case string:
		if v != "" {
			blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: v})
		}
	case []AnthropicContentBlock:
		blocks = append(blocks, v...)
	case []ContentBlock:
		for _, b := range v {
			blocks = append(blocks, AnthropicContentBlock{Type: b.Type, Text: b.Text})
		}
	case []any:
		for _, item := range v {
			data, err := json.Marshal(item)
			if err != nil {
				continue
			}
			var b AnthropicContentBlock
			if err := json.Unmarshal(data, &b); err != nil {
				continue
			}
			blocks = append(blocks, b)
		}
	}

	for _, tc := range m.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if !json.Valid(input) {
			input = json.RawMessage(`{}`)
		}
		blocks = append(blocks, AnthropicContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}

	return blocks
}

func internalContentToAnthropic(content any) any {
	switch v := content.(type) {
	case string:
		return v
	case []AnthropicContentBlock:
		return v
	case []ContentBlock:
		blocks := make([]AnthropicContentBlock, len(v))
		for i, b := range v {
			blocks[i] = AnthropicContentBlock{Type: b.Type, Text: b.Text}
		}
		return blocks
	case []any:
		return v
	}
	return extractTextContent(content)
}

// decodeAnthropicBlocks attempts to decode content as a []AnthropicContentBlock.
// Content decoded from JSON via encoding/json will arrive as []any with map[string]any items.
func decodeAnthropicBlocks(content any) ([]AnthropicContentBlock, bool) {
	switch v := content.(type) {
	case []AnthropicContentBlock:
		return v, true
	case []any:
		blocks := make([]AnthropicContentBlock, 0, len(v))
		for _, item := range v {
			data, err := json.Marshal(item)
			if err != nil {
				continue
			}
			var b AnthropicContentBlock
			if err := json.Unmarshal(data, &b); err != nil {
				continue
			}
			blocks = append(blocks, b)
		}
		return blocks, true
	}
	return nil, false
}

func extractAnthropicText(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []AnthropicContentBlock:
		var parts []string
		for _, b := range val {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	case []any:
		var parts []string
		for _, item := range val {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok && t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}
