package proxy

import (
	"encoding/json"
	"log/slog"
	"strings"

	"lazy-vllm-proxy/internal/config"
)

// Server holds the state for all HTTP handlers.
type Server struct {
	Backends     []config.Backend
	RoutingRules []config.RouteRule
	LevelVar     *slog.LevelVar
}

// resolveBackend returns the upstream URL for a given model name.
// If no exact match is found, falls back to the first backend.
func resolveBackend(backends []config.Backend, model string) string {
	for _, r := range backends {
		if r.Name == model {
			return r.URL
		}
	}
	if len(backends) > 0 {
		return backends[0].URL
	}
	return ""
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

// extractModel returns the model name from a request body, or "".
func extractModel(body []byte) string {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return ""
	}
	if m, ok := top["model"]; ok {
		var model string
		if err := json.Unmarshal(m, &model); err == nil {
			return model
		}
	}
	return ""
}

const flashSuffix = "-FLASH"

// stripFlash removes the -FLASH suffix from a model name if present.
func stripFlash(name string) string {
	if strings.HasSuffix(name, flashSuffix) {
		return strings.TrimSuffix(name, flashSuffix)
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
