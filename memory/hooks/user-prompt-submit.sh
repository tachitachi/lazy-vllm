#!/bin/bash
INPUT=$(cat)
PROMPT=$(echo "$INPUT" | jq -r '.prompt // empty' 2>/dev/null)

[ -z "$PROMPT" ] && exit 0

THRESHOLD="${MEMORY_THRESHOLD:-0.5}"
MEMORY_URL="${MEMORY_URL:-http://localhost:8020}"

PAYLOAD=$(jq -n --arg q "$PROMPT" --argjson t "$THRESHOLD" '{"query": $q, "threshold": $t}')

RESULT=$(curl -s -f -X POST "${MEMORY_URL}/search" \
    -H "Content-Type: application/json" \
    -d "$PAYLOAD" \
    --max-time 2) || exit 0

[ -n "$RESULT" ] && echo "$RESULT"
