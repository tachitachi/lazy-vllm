package proxy

import (
	"encoding/json"
	"log/slog"
	"strings"

	"lazy-vllm-proxy/internal/config"
)


// Server holds the state for all HTTP handlers.
type Server struct {
	Backends               []config.Backend
	Providers              []config.Provider
	RoutingRules           []config.RouteRule
	LevelVar               *slog.LevelVar
	MemoryIngestURL        string
	ThinkingBudgetFraction float64
}

// lookupProvider returns the Provider for a given name, or false if not found.
func lookupProvider(providers []config.Provider, name string) (config.Provider, bool) {
	for _, p := range providers {
		if p.Name == name {
			return p, true
		}
	}
	return config.Provider{}, false
}

// modelFromBody reads the model field from a JSON body without modifying the body.
func modelFromBody(body []byte) string {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return ""
	}
	m, _ := obj["model"].(string)
	return m
}

// resolveBackend returns the upstream URL for a given model name.
// If no exact match is found, falls back to the first backend.
func resolveBackend(backends []config.Backend, model string) string {
	return findBackend(backends, model).URL
}

// findBackend returns the Backend for a given model name.
// If no exact match is found, falls back to the first backend.
func findBackend(backends []config.Backend, model string) config.Backend {
	for _, b := range backends {
		if b.Name == model {
			return b
		}
	}
	if len(backends) > 0 {
		return backends[0]
	}
	return config.Backend{}
}

// uniqueURLs returns distinct backend URLs, preserving insertion order.
func uniqueURLs(backends []config.Backend) []string {
	seen := make(map[string]struct{})
	var urls []string
	for _, r := range backends {
		if _, ok := seen[r.URL]; !ok {
			seen[r.URL] = struct{}{}
			urls = append(urls, r.URL)
		}
	}
	return urls
}

// extractModel returns the model name from the request body and a cleaned body.
// For -FLASH models, the cleaned body has the model field updated to strip the suffix.
func extractModel(body []byte) (string, []byte) {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return "", body
	}
	model, ok := obj["model"].(string)
	if !ok || model == "" {
		return "", body
	}
	if before, ok0 := strings.CutSuffix(model, flashSuffix); ok0 {
		obj["model"] = before
		cleaned, _ := json.Marshal(obj)
		if len(cleaned) == 0 {
			cleaned = body
		}
		return model, cleaned
	}
	return model, body
}

const flashSuffix = "-FLASH"

// stripFlash removes the -FLASH suffix from a model name if present.
func stripFlash(name string) string {
	if before, ok := strings.CutSuffix(name, flashSuffix); ok {
		return before
	}
	return name
}

// isFlashModel checks if a model name is the flash variant.
func isFlashModel(name string) bool {
	return strings.HasSuffix(name, flashSuffix)
}

// injectDisableThinking adds chat_template_kwargs to disable thinking in the request body.
// Also strips the -FLASH suffix from the model name so the backend recognizes it.
func injectDisableThinking(body []byte) []byte {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	obj["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	if model, ok := obj["model"].(string); ok && isFlashModel(model) {
		obj["model"] = stripFlash(model)
	}
	data, _ := json.Marshal(obj)
	if len(data) == 0 {
		return body
	}
	return data
}

// effortTokenBudget maps an effort string to a token budget.
// Returns (0, true) for "none" — caller should disable thinking entirely.
// Returns (32000, false) for empty/unknown values — treated as "max".
func effortTokenBudget(effort string) (tokens int, disable bool) {
	switch strings.ToLower(effort) {
	case "none":
		return 0, true
	case "minimal":
		return 128, false
	case "low":
		return 256, false
	case "medium":
		return 1024, false
	case "high":
		return 4096, false
	case "xhigh":
		return 8192, false
	default: // "max" and missing both map to 32000
		return 32000, false
	}
}

// requestDisableThinking checks the Anthropic root-level "thinking" field.
// Returns true when thinking.type == "disabled", signaling that the client
// explicitly opted out of thinking and the proxy should inject the disable
// mutation for local backends.
func requestDisableThinking(body []byte) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return false
	}
	raw, ok := obj["thinking"]
	if !ok {
		return false
	}
	var thinking map[string]json.RawMessage
	if err := json.Unmarshal(raw, &thinking); err != nil {
		return false
	}
	var t string
	if err := json.Unmarshal(thinking["type"], &t); err != nil {
		return false
	}
	return t == "disabled"
}

// extractEffort reads reasoning_effort (OpenAI) or output_config.effort (Anthropic).
// Returns "" if neither field is present.
func extractEffort(body []byte) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return ""
	}
	if raw, ok := obj["reasoning_effort"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	if raw, ok := obj["output_config"]; ok {
		var cfg map[string]json.RawMessage
		if json.Unmarshal(raw, &cfg) == nil {
			if effortRaw, ok := cfg["effort"]; ok {
				var s string
				if json.Unmarshal(effortRaw, &s) == nil {
					return s
				}
			}
		}
	}
	return ""
}

// extractMaxTokens reads max_tokens from the body. Returns defaultValue if absent or ≤ 0.
func extractMaxTokens(body []byte, defaultValue int) int {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return defaultValue
	}
	if raw, ok := obj["max_tokens"]; ok {
		var n int
		if json.Unmarshal(raw, &n) == nil && n > 0 {
			return n
		}
	}
	return defaultValue
}

// computeThinkingBudget returns the thinking_token_budget to inject for a local request.
// Returns (0, true) when thinking should be disabled entirely (effort="none").
func computeThinkingBudget(body []byte, fraction float64) (budget int, disable bool) {
	effort := extractEffort(body)
	effortBudget, shouldDisable := effortTokenBudget(effort)
	if shouldDisable {
		return 0, true
	}
	maxTokens := extractMaxTokens(body, 32000)
	fractionCap := int(float64(maxTokens) * fraction)
	if effortBudget < fractionCap {
		return effortBudget, false
	}
	return fractionCap, false
}

// injectThinkingTokenBudget adds thinking_token_budget at the top level of the request JSON.
func injectThinkingTokenBudget(body []byte, budget int) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}
	budgetJSON, err := json.Marshal(budget)
	if err != nil {
		return nil, err
	}
	top["thinking_token_budget"] = budgetJSON
	return json.Marshal(top)
}
