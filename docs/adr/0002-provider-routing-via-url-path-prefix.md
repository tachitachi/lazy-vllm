# ADR 0002: Provider Routing via URL Path Prefix

**Status:** Accepted  
**Date:** 2026-05-21

## Context

The proxy previously required manually swapping `BACKENDS_MAP` environment variables in docker-compose.yml to switch between local vLLM models and external API providers (e.g. Anthropic). This forced the operator to choose one upstream at deploy time, making it impossible to run `claude-l` (Anthropic API) and `claude-local` (local vLLM) against the same proxy instance simultaneously.

## Decision

Provider selection is encoded in the URL path prefix, immediately before the existing user/project attribution segments:

```
/provider/{name}/user/{user}/project/{base64url_project}/v1/...
```

Two valid path shapes are enforced by `AttributionMiddleware`:
1. **Fully attributed** — all three segments present
2. **Anonymous** — no prefix at all (always routes to `"local"`)

Any partial path (provider without user/project, or user/project without provider) returns 400 Bad Request.

`"local"` is a reserved provider name meaning "resolve via `BACKENDS_MAP`". All other names are looked up in `PROVIDERS_MAP`. An unknown provider name returns 400.

## Alternatives Rejected

**HTTP header** (`X-Provider: anthropic`): Cannot be injected per-provider via `ANTHROPIC_BASE_URL` alone in Claude Code. Headers require client-side code changes beyond base URL configuration.

**Query parameter** (`/v1/messages?provider=anthropic`): Stripped by some HTTP clients and proxies; pollutes the upstream request target.

**Hostname/subdomain routing** (`anthropic.proxy.local/v1/messages`): Requires DNS configuration and TLS cert changes; not portable across environments.

## Consequences

- `PROVIDERS_MAP` is a new optional env var: `[{"name":"anthropic","url":"https://api.anthropic.com","count_tokens":false}]`
- `BACKENDS_MAP` remains required (anonymous and local-attributed requests still resolve via it)
- `"local"` cannot appear in `PROVIDERS_MAP` — startup error if it does
- For non-local providers: model name passes through untouched, FLASH injection skipped, routing rules skipped
- `claude-l` defaults to `PROVIDER=anthropic`; `docker-run-claude.sh` defaults to `PROVIDER=local`
- Provider stored in SQLite `sessions.provider` column (schema v6) and shown in the logs UI
- Same rationale as ADR 0001 — URL path works with any base-URL-style client config

See also: [[docs/adr/0001-user-project-attribution-via-url-path-prefix.md]]
