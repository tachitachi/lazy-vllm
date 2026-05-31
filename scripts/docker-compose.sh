#!/bin/bash
set -euo pipefail

# Tailscale hostname for the fast-qwen node (override via env: FAST_QWEN_HOST)
FAST_QWEN_HOST="${FAST_QWEN_HOST:-}"

if [ -z "$FAST_QWEN_HOST" ]; then
  echo "ERROR: Set FAST_QWEN_HOST to the Tailscale hostname of the fast-qwen node."
  exit 1
fi

# Resolve the Tailscale IP
if command -v tailscale &>/dev/null; then
  FAST_QWEN_IP=$(tailscale ip --4=true "$FAST_QWEN_HOST")
elif command -v dig &>/dev/null; then
  FAST_QWEN_IP=$(dig +short "${FAST_QWEN_HOST}.ts.net")
else
  echo "ERROR: Neither tailscale nor dig found — cannot resolve fast-qwen IP."
  exit 1
fi

if [ -z "$FAST_QWEN_IP" ]; then
  echo "ERROR: Failed to resolve $FAST_QWEN_HOST's Tailscale IP."
  exit 1
fi

export FAST_QWEN_IP

docker compose "$@"
