package agent

import (
	"encoding/json"
	"net/http"
)

// StripAllThinking removes thinking content from a slice of messages.
// It handles two formats:
//   - Gemma4 text format: <think>...</think> tags within string content
//   - Anthropic block format: content blocks with type "thinking" or "redacted_thinking"
//
// Returns a new slice; the input is not modified.
func StripAllThinking(msgs []ChatMessage) []ChatMessage {
	out := make([]ChatMessage, len(msgs))
	for i, m := range msgs {
		out[i] = StripMessageThinking(m)
	}
	return out
}

func StripMessageThinking(m ChatMessage) ChatMessage {
	result := m
	result.ReasoningContent = "" // vLLM OpenAI format thinking field
	switch v := m.Content.(type) {
	case string:
		_ = v // no <think> tags in vLLM OpenAI responses; nothing to strip from content
	case []any:
		filtered := make([]any, 0, len(v))
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				filtered = append(filtered, item)
				continue
			}
			t, _ := block["type"].(string)
			if t == "thinking" || t == "redacted_thinking" {
				continue
			}
			filtered = append(filtered, item)
		}
		result.Content = filtered
	case []ContentBlock:
		filtered := make([]ContentBlock, 0, len(v))
		for _, b := range v {
			if b.Type == "thinking" || b.Type == "redacted_thinking" {
				continue
			}
			filtered = append(filtered, b)
		}
		result.Content = filtered
	case []AnthropicContentBlock:
		filtered := make([]AnthropicContentBlock, 0, len(v))
		for _, b := range v {
			if b.Type == "thinking" || b.Type == "redacted_thinking" {
				continue
			}
			filtered = append(filtered, b)
		}
		result.Content = filtered
	case json.RawMessage:
		// Try to decode as block array
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(v, &blocks) == nil {
			filtered := make([]map[string]json.RawMessage, 0, len(blocks))
			for _, b := range blocks {
				rawType, ok := b["type"]
				if !ok {
					filtered = append(filtered, b)
					continue
				}
				var t string
				if json.Unmarshal(rawType, &t) == nil && (t == "thinking" || t == "redacted_thinking") {
					continue
				}
				filtered = append(filtered, b)
			}
			encoded, err := json.Marshal(filtered)
			if err == nil {
				result.Content = json.RawMessage(encoded)
			}
		}
	}
	return result
}

// applyWindow returns the last n messages. If n <= 0, returns msgs unchanged.
func applyWindow(msgs []ChatMessage, n int) []ChatMessage {
	if n <= 0 || len(msgs) <= n {
		return msgs
	}
	return msgs[len(msgs)-n:]
}

// DeepCopy returns an independent copy of pctx. Slices and maps are deep-copied
// so branches can diverge without affecting the parent or each other.
func (p *PipelineCtx) DeepCopy() *PipelineCtx {
	c := &PipelineCtx{
		Stream:        p.Stream,
		Format:        p.Format,
		ModelOverride: p.ModelOverride,
	}

	if p.OriginalBody != nil {
		c.OriginalBody = make([]byte, len(p.OriginalBody))
		copy(c.OriginalBody, p.OriginalBody)
	}

	if p.OriginalHeaders != nil {
		c.OriginalHeaders = p.OriginalHeaders.Clone()
	} else {
		c.OriginalHeaders = make(http.Header)
	}

	if p.Messages != nil {
		c.Messages = make([]ChatMessage, len(p.Messages))
		copy(c.Messages, p.Messages)
	}

	if p.NodeOutputs != nil {
		c.NodeOutputs = make(map[string]string, len(p.NodeOutputs))
		for k, v := range p.NodeOutputs {
			c.NodeOutputs[k] = v
		}
	} else {
		c.NodeOutputs = make(map[string]string)
	}

	if p.ThinkingMode != nil {
		v := *p.ThinkingMode
		c.ThinkingMode = &v
	}

	return c
}
