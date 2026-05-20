#!/bin/bash
INPUT=$(cat)
PROMPT=$(echo "$INPUT" | jq -r '.prompt // empty' 2>/dev/null)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty' 2>/dev/null)

[ -z "$PROMPT" ] && exit 0
[ -z "$SESSION_ID" ] && exit 0

THRESHOLD="${MEMORY_THRESHOLD:-0.5}"
MEMORY_URL="${MEMORY_URL:-http://localhost:8020}"
SEEN_FILE="${TMPDIR:-/tmp}/memory-seen-${SESSION_ID}"

PROJECT=$(echo -n "$PWD" | base64 -w 0 | tr '+/' '-_' | tr -d '=')

# Build exclude list from IDs already injected this session
if [ -f "$SEEN_FILE" ]; then
    EXCLUDE_IDS=$(jq -Rsc 'split("\n") | map(select(length > 0))' < "$SEEN_FILE")
else
    EXCLUDE_IDS="[]"
fi

PAYLOAD=$(jq -n \
    --arg q "$PROMPT" \
    --arg u "$USER" \
    --arg p "$PROJECT" \
    --argjson t "$THRESHOLD" \
    --argjson e "$EXCLUDE_IDS" \
    '{"query": $q, "threshold": $t, "exclude_ids": $e, "user": $u, "project": $p}')

RESPONSE=$(curl -s -f -X POST "${MEMORY_URL}/search" \
    -H "Content-Type: application/json" \
    -d "$PAYLOAD" \
    --max-time 2) || exit 0

TEXT=$(echo "$RESPONSE" | jq -r '.text // empty' 2>/dev/null)
[ -z "$TEXT" ] && exit 0

# Record injected IDs so they aren't injected again this session
echo "$RESPONSE" | jq -r '.ids[]? // empty' 2>/dev/null >> "$SEEN_FILE"

echo "$TEXT"
