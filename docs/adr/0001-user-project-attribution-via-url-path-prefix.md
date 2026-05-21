# User/project attribution via URL path prefix

Sessions need to be tagged with the user and project that initiated them so logs and memory search can be filtered by user/project. We encode this as a URL path prefix: clients are configured with a base URL of the form `http://proxy/user/{user}/project/{base64url_project}`, which the proxy's middleware extracts and strips before dispatching to the existing handlers.

The alternative was HTTP headers (e.g. `X-Lazy-User`, `X-Lazy-Project`). Headers are cleaner semantically but harder to configure in practice: Claude Code sets attribution via `ANTHROPIC_BASE_URL` only — it has no mechanism for injecting arbitrary headers per-project without additional tooling. The path-prefix approach works with any client that supports a configurable base URL, requiring only a one-line wrapper script (`claude-l`) that sets `ANTHROPIC_BASE_URL` with the encoded prefix.

## Considered options

**Headers** — rejected: Claude Code and most API clients don't expose per-call custom header injection from a base URL setting alone. Would require a separate proxy shim or client patching.

**Query parameters** — rejected: clients would need to append `?user=...&project=...` to every request, not just the base URL. Not supported by the base URL configuration pattern.

**Path prefix (chosen)**: compatible with any `ANTHROPIC_BASE_URL`-style configuration. The project directory is encoded as base64url (RFC 4648 §5, no padding) to eliminate `/` characters that would fragment the path segment.

## Consequences

- Clients that are not configured with the prefix work unchanged; their sessions are stored with `user=NULL`, `project=NULL` (anonymous sessions).
- The `claude-l` wrapper script encodes `$USER` and `base64url($PWD)` into the base URL and passes all args to `claude`.
- The proxy middleware must rewrite the request URL before dispatch and change `forwardRequest` to use `r.URL.RequestURI()` instead of `r.RequestURI` so the upstream never sees the prefix.
- The memory server's `/search` endpoint gains optional `user`/`project` where-filters; the `user-prompt-submit.sh` hook passes these when known.
