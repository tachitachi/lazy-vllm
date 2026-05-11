package agent

import "strings"

func ResolveBackend(backends []BackendRule, model string) string {
	for _, rule := range backends {
		if strings.HasPrefix(model, rule.Prefix) {
			return rule.URL
		}
	}
	// Fallback to first backend
	if len(backends) > 0 {
		return backends[0].URL
	}
	return ""
}
