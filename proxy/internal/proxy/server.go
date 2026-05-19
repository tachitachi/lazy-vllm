package proxy

import (
	"encoding/json"
	"log/slog"
	"strings"

	"lazy-vllm-proxy/internal/config"
)

// Server holds the state for all HTTP handlers.
type Server struct {
	Backends        []config.Backend
	RoutingRules    []config.RouteRule
	LevelVar        *slog.LevelVar
	MemoryIngestURL string
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
