package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const routingPrompt = "You are a query router. Classify the user's latest message as DIRECT or REASONING.\n\n" +
	"DIRECT: Only the simplest possible requests — greetings, basic small talk, or trivially " +
	"obvious one-fact answers (e.g. 'Hi', 'What color is the sky?').\n\n" +
	"REASONING: Everything else — explanations, analysis, coding, math, comparisons, " +
	"multi-step tasks, writing, summaries, advice, debugging, or any question where " +
	"thinking step-by-step would improve the answer.\n\n" +
	"When in doubt, choose REASONING."

// classify sends the message window to vLLM with thinking disabled and structured
// output constrained to ["DIRECT", "REASONING"] via structured_outputs.choice.
// On error it returns true (fail-open: matches vLLM server default of enable_thinking=true).
func classify(ctx context.Context, messages []ChatMessage, cfg Config, authHeader string) (bool, error) {
	start := time.Now()
	defer func() { classifyDurationSeconds.Observe(time.Since(start).Seconds()) }()

	msgs := make([]ChatMessage, 0, len(messages)+1)
	msgs = append(msgs, ChatMessage{Role: "system", Content: routingPrompt})
	msgs = append(msgs, messages...)

	payload := map[string]any{
		"model":                cfg.ModelName,
		"messages":             msgs,
		"max_tokens":           16,
		"stream":               false,
		"structured_outputs":   map[string]any{"choice": []string{"DIRECT", "REASONING"}},
		"chat_template_kwargs": map[string]bool{"enable_thinking": false},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return true, fmt.Errorf("classify marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.VLLMBaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return true, fmt.Errorf("classify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return true, fmt.Errorf("classify http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return true, fmt.Errorf("classify read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return true, fmt.Errorf("classify status %d: %s", resp.StatusCode, respBody)
	}

	var result chatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return true, fmt.Errorf("classify unmarshal: %w", err)
	}
	if len(result.Choices) == 0 {
		return true, fmt.Errorf("classify: no choices in response")
	}

	content, ok := result.Choices[0].Message.Content.(string)
	if !ok {
		return true, fmt.Errorf("classify: unexpected content type")
	}
	slog.Info("classify response", "content", content)
	return strings.TrimSpace(strings.ToUpper(content)) == "REASONING", nil
}
