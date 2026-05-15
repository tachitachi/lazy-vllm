#!/usr/bin/env bash
set -e

IMAGE="claude-local-dev"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Auto-build if image doesn't exist
if ! docker inspect "$IMAGE" >/dev/null 2>&1; then
    echo "==> Building $IMAGE (first run only)..."
    docker build -f "$SCRIPT_DIR/Dockerfile.claude" -t "$IMAGE" "$SCRIPT_DIR"
fi

# Detect host user/group for -u flag (bind mount files need matching ownership)
HOST_UID=$(id -u)
HOST_GID=$(id -g)

# Run Claude Code in Docker.
# --network host: container sees host localhost:8002 directly.
# -v ...:/workspace: bind mount so IDE edits sync with the container.
# -v claude-config: persist Claude Code session data across restarts.
# -v go-cache: persist Go module cache across sessions.
docker run -it --rm --network host \
    -u "$HOST_UID:$HOST_GID" \
    -e HOME=/home/ubuntu \
    -v "$(pwd):/workspace" \
    -v claude-home:/home/ubuntu \
    -v claude-npm-cache:/home/ubuntu/.npm \
    -v claude-cache:/home/ubuntu/.cache \
    -v claude-go-cache:/home/ubuntu/go/pkg/mod \
    -e ANTHROPIC_BASE_URL=http://localhost:8002 \
    -e ANTHROPIC_API_KEY=dummy \
    -e ANTHROPIC_AUTH_TOKEN=dummy \
    -e ANTHROPIC_DEFAULT_OPUS_MODEL=Qwen3.6-27B-Text-NVFP4-MTP \
    -e ANTHROPIC_DEFAULT_SONNET_MODEL=Qwen3.6-35B-A3B-NVFP4-FLASH \
    -e ANTHROPIC_DEFAULT_HAIKU_MODEL=Qwen3.6-35B-A3B-NVFP4-FLASH \
    -e CLAUDE_CODE_AUTO_COMPACT_WINDOW=262144 \
    -e GIT_CONFIG_COUNT=3 \
    -e GIT_CONFIG_KEY_0=user.name \
    -e GIT_CONFIG_VALUE_0="$(git config user.name)" \
    -e GIT_CONFIG_KEY_1=user.email \
    -e GIT_CONFIG_VALUE_1="$(git config user.email)" \
    -e GIT_CONFIG_KEY_2=safe.directory \
    -e GIT_CONFIG_VALUE_2=/workspace \
    "$IMAGE" "$@"
