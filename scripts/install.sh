#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"
IMAGE="claude-local-dev"
SYMLINK_NAME="claude-local"
INSTALL_DIR="$HOME/.local/bin"

echo "==> Building Docker image: $IMAGE"
docker build -f "$REPO_DIR/Dockerfile.claude" -t "$IMAGE" "$REPO_DIR"

mkdir -p "$INSTALL_DIR"

SYMLINK_PATH="$INSTALL_DIR/$SYMLINK_NAME"
if [ -L "$SYMLINK_PATH" ]; then
    echo "==> Updating existing symlink at $SYMLINK_PATH"
    ln -sf "$SCRIPT_DIR/docker-run-claude.sh" "$SYMLINK_PATH"
elif [ -e "$SYMLINK_PATH" ]; then
    echo "ERROR: $SYMLINK_PATH exists and is not a symlink. Remove it manually." >&2
    exit 1
else
    echo "==> Creating symlink: $SYMLINK_PATH -> $SCRIPT_DIR/docker-run-claude.sh"
    ln -s "$SCRIPT_DIR/docker-run-claude.sh" "$SYMLINK_PATH"
fi

if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
    echo ""
    echo "NOTE: $INSTALL_DIR is not in your PATH."
    echo "Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
    echo "  export PATH=\"\$HOME/.local/bin:\$PATH\""
fi

echo ""
echo "Done. Run 'claude-local' from any directory to start Claude with your local models."
