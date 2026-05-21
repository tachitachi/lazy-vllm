# lazy-vllm

A self-hosted LLM proxy with session logging, semantic memory, and agent graph support on top of vLLM/Gemma4.

## Language

### Proxy & Logging

**Session**:
A single LLM API call captured by the proxy — one inbound request and its response, stored as a row in the SQLite `sessions` table with a generated UUID.
_Avoid_: request, log entry, conversation turn

**Attribution**:
The user and project metadata attached to a session. Present only when the client uses an attributed base URL; absent (null) for anonymous sessions.
_Avoid_: context, metadata, identity

**User**:
The OS-level username (`$USER`) of the client that initiated a session, extracted from the URL path prefix.
_Avoid_: principal, identity, account

**Project**:
The working directory of the client that initiated a session, decoded from the base64url path segment in the URL. Stored as a plain filesystem path (e.g. `/home/aaron/GitHub/my-app`).
_Avoid_: workspace, directory, cwd

**Attributed session**:
A session whose user and project are known (non-null). Contrast with **anonymous session**, where the client did not use an attributed base URL.

**Flash variant**:
A pseudo-model named with a `-FLASH` suffix (e.g. `gemma-FLASH`) that routes to the same backend as the base model but with thinking mode disabled via `chat_template_kwargs`.
_Avoid_: fast model, non-thinking model, instant model

**Provider**:
The upstream API service selected for a session — either `"local"` (routes to a BACKENDS_MAP entry by model name) or a named entry from PROVIDERS_MAP (routes to a fixed external URL). Carried in the URL path prefix; stored in the `provider` column of the sessions table.
_Avoid_: backend, upstream, service

**Terminal session**:
A session whose final message is an assistant message with no tool calls, indicating the model returned control to the user.
_Avoid_: complete session, final session, done session

**Intermediate session**:
A session whose final message contains tool calls, representing a turn in an ongoing agentic loop. Contrast with **terminal session**.
_Avoid_: incomplete session, in-progress session, non-terminal session

**Backend**:
A local vLLM server instance, identified by name and URL in `BACKENDS_MAP`. Used only when provider is `"local"`.
_Avoid_: upstream, server, endpoint

### Memory

**Observation (obs)**:
A structured metadata block the model appends to its response containing `type`, `topic`, `claim`, `rationale`, and `scope` fields. The proxy extracts this and stores it as the session summary.
_Avoid_: summary, note, annotation

**Memory**:
A ChromaDB document derived from a session's observation, indexed for vector similarity search. Only sessions with `type=decision` or `type=fact` observations become memories.
_Avoid_: knowledge, record, embedding

## Example dialogue

> "Why does this session have a null user?"
> "It's an anonymous session — the client didn't use an attributed base URL. Attribution is only present when the client is configured with `/provider/{name}/user/{user}/project/{project}/` in its base URL."

> "How do I route a session to Anthropic instead of local models?"
> "Set PROVIDER=anthropic before calling claude-l, or configure ANTHROPIC_BASE_URL to include `/provider/anthropic/` as the path prefix. The proxy looks up 'anthropic' in PROVIDERS_MAP and routes to that URL."

> "What's the project field — is it a name or a path?"
> "It's the decoded working directory path, e.g. `/home/aaron/GitHub/lazy-vllm`. The path arrives base64url-encoded in the URL and the proxy decodes it before storing."

> "When does a session become a memory?"
> "Only when its observation has `type=decision` or `type=fact`. The proxy extracts the obs block from the response, stores it as the session summary in SQLite, then forwards it to the memory server which upserts it into ChromaDB."
