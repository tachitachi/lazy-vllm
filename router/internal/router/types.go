package router

import (
	"encoding/json"
	"strings"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// minimalRequest is used only to extract messages and stream flag.
// The full request body is handled as map[string]json.RawMessage to
// preserve all unknown fields during injection.
type minimalRequest struct {
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatChoice struct {
	Message ChatMessage `json:"message"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

type AnthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type AnthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []AnthropicContentBlock
}

type minimalAnthropicRequest struct {
	System   any               `json:"system"` // string or []AnthropicContentBlock
	Messages []AnthropicMessage `json:"messages"`
	Stream   bool              `json:"stream"`
}

// anthropicMessagesToChat converts an Anthropic Messages request into the
// []ChatMessage slice that classify() expects (OpenAI format, text-only).
func anthropicMessagesToChat(system any, messages []AnthropicMessage) []ChatMessage {
	var result []ChatMessage
	if s := extractText(system); s != "" {
		result = append(result, ChatMessage{Role: "system", Content: s})
	}
	for _, m := range messages {
		result = append(result, ChatMessage{Role: m.Role, Content: extractText(m.Content)})
	}
	return result
}

// extractText returns plain text from a JSON-decoded value that is either a
// string or a slice of Anthropic content blocks.
func extractText(v any) string {
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
		return strings.Join(parts, " ")
	}
	return ""
}

// injectThinkingMode adds chat_template_kwargs: {"enable_thinking": <enable>}
// at the top level of the request JSON, preserving all other fields.
func injectThinkingMode(body []byte, enable bool) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}
	tplKwargs, err := json.Marshal(map[string]bool{"enable_thinking": enable})
	if err != nil {
		return nil, err
	}
	top["chat_template_kwargs"] = tplKwargs
	return json.Marshal(top)
}
