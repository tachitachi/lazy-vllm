#!/usr/bin/env bash
set -e

SYMLINK_NAME="claude-local"
INSTALL_DIR="$HOME/.local/bin"
SYMLINK_PATH="$INSTALL_DIR/$SYMLINK_NAME"

if [ -L "$SYMLINK_PATH" ]; then
    rm "$SYMLINK_PATH"
    echo "==> Removed symlink: $SYMLINK_PATH"
elif [ -e "$SYMLINK_PATH" ]; then
    echo "ERROR: $SYMLINK_PATH exists but is not a symlink. Remove it manually." >&2
    exit 1
else
    echo "==> Nothing to remove ($SYMLINK_PATH not found)."
fi
