package main

import "encoding/json"

type ChatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// Minimal decode target — used only to extract messages and stream flag.
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
