package proxy

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"

	"lazy-vllm-proxy/internal/config"
)

type ctxKeyUser     struct{}
type ctxKeyProject  struct{}
type ctxKeyProvider struct{}

// UserFromCtx returns the attributed user from the request context, or empty string.
func UserFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyUser{}).(string)
	return v
}

// ProjectFromCtx returns the attributed project (decoded path) from the request context, or empty string.
func ProjectFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyProject{}).(string)
	return v
}

// ProviderFromCtx returns the provider name from the request context, or empty string.
func ProviderFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyProvider{}).(string)
	return v
}

// extractAttribution parses /provider/{provider}/user/{user}/project/{base64url_project}{/rest}.
// Returns provider, user, decoded project, remaining path (with leading /), and ok=true on full match.
func extractAttribution(path string) (provider, user, project, remaining string, ok bool) {
	rest, found := strings.CutPrefix(path, "/provider/")
	if !found {
		return
	}
	idx := strings.Index(rest, "/")
	if idx < 0 {
		return
	}
	provider = rest[:idx]
	rest = rest[idx+1:]

	rest, found = strings.CutPrefix(rest, "user/")
	if !found {
		return
	}
	idx = strings.Index(rest, "/")
	if idx < 0 {
		return
	}
	user = rest[:idx]
	rest = rest[idx+1:]

	rest, found = strings.CutPrefix(rest, "project/")
	if !found {
		return
	}
	idx = strings.Index(rest, "/")
	var encodedProject string
	if idx < 0 {
		encodedProject = rest
		remaining = "/"
	} else {
		encodedProject = rest[:idx]
		remaining = rest[idx:] // includes leading /
	}
	if provider == "" || user == "" || encodedProject == "" {
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encodedProject)
	if err != nil {
		project = encodedProject // graceful fallback
	} else {
		project = string(decoded)
	}
	ok = true
	return
}

// AttributionMiddleware enforces the two-mode URL contract:
//   - Fully attributed: /provider/{name}/user/{user}/project/{base64url}/...
//   - Anonymous:        /v1/... (or any path not starting with /provider/ or /user/)
//
// Partial paths (/provider/... without user/project, or /user/... without provider) → 400.
// Unknown provider names → 400.
func AttributionMiddleware(providers []config.Provider) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// Old-format paths (user/project without provider) are no longer supported.
			if strings.HasPrefix(path, "/user/") {
				http.Error(w, "partial attribution path: include /provider/{name}/ prefix", http.StatusBadRequest)
				return
			}

			if strings.HasPrefix(path, "/provider/") {
				provider, user, project, remaining, ok := extractAttribution(path)
				if !ok {
					http.Error(w, "malformed attribution path: expected /provider/{name}/user/{user}/project/{base64url}/...", http.StatusBadRequest)
					return
				}
				if provider != "local" {
					if _, found := lookupProvider(providers, provider); !found {
						http.Error(w, "unknown provider: "+provider, http.StatusBadRequest)
						return
					}
				}
				ctx := context.WithValue(r.Context(), ctxKeyProvider{}, provider)
				ctx = context.WithValue(ctx, ctxKeyUser{}, user)
				ctx = context.WithValue(ctx, ctxKeyProject{}, project)
				r2 := r.Clone(ctx)
				r2.URL.Path = remaining
				r2.URL.RawPath = ""
				r2.RequestURI = r2.URL.RequestURI()
				next.ServeHTTP(w, r2)
				return
			}

			// Anonymous path — no attribution.
			next.ServeHTTP(w, r)
		})
	}
}
