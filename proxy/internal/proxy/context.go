package proxy

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
)

type ctxKeyUser    struct{}
type ctxKeyProject struct{}

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

// extractUserProject parses /user/{user}/project/{base64url_project}{/rest} from path.
// Returns the user, decoded project, remaining path (with leading /), and ok=true on match.
func extractUserProject(path string) (user, project, remaining string, ok bool) {
	rest, found := strings.CutPrefix(path, "/user/")
	if !found {
		return
	}
	idx := strings.Index(rest, "/")
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
	if user == "" || encodedProject == "" {
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encodedProject)
	if err != nil {
		project = encodedProject // graceful fallback: use raw value
	} else {
		project = string(decoded)
	}
	ok = true
	return
}

// UserProjectMiddleware strips the /user/{user}/project/{base64url_project}/ prefix from
// incoming requests, decodes user and project into the request context, and rewrites the
// request URL before dispatching to next. Requests without the prefix pass through unchanged
// with empty user/project in context (anonymous sessions).
func UserProjectMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, project, stripped, ok := extractUserProject(r.URL.Path)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyUser{}, user)
		ctx = context.WithValue(ctx, ctxKeyProject{}, project)
		r2 := r.Clone(ctx)
		r2.URL.Path = stripped
		r2.URL.RawPath = ""
		r2.RequestURI = r2.URL.RequestURI()
		next.ServeHTTP(w, r2)
	})
}
