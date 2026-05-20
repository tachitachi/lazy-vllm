#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HOOK_SCRIPT="$SCRIPT_DIR/hooks/user-prompt-submit.sh"
GLOBAL=false
TARGET_DIR=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --global)  GLOBAL=true; shift ;;
        --project) TARGET_DIR="$2"; shift 2 ;;
        *)         echo "Usage: $0 [--global] [--project <path>]"; exit 1 ;;
    esac
done

if $GLOBAL; then
    CONFIG_DIR="$HOME/.claude"
    SETTINGS_FILE="$CONFIG_DIR/settings.json"
else
    TARGET_DIR="${TARGET_DIR:-$(pwd)}"
    CONFIG_DIR="$TARGET_DIR/.claude"
    mkdir -p "$CONFIG_DIR"
    SETTINGS_FILE="$CONFIG_DIR/settings.local.json"
fi

CLAUDE_MD="$CONFIG_DIR/CLAUDE.md"

# Merge hook entry into settings JSON
python3 - "$SETTINGS_FILE" "$HOOK_SCRIPT" <<'EOF'
import sys, json, os

settings_path = sys.argv[1]
hook_script = sys.argv[2]

settings = {}
if os.path.exists(settings_path):
    with open(settings_path) as f:
        settings = json.load(f)

hook_entry = {"hooks": [{"type": "command", "command": hook_script}]}
hooks = settings.setdefault("hooks", {})
submit_hooks = hooks.setdefault("UserPromptSubmit", [])

# Avoid duplicates
if not any(
    any(h.get("command") == hook_script for h in entry.get("hooks", []))
    for entry in submit_hooks
):
    submit_hooks.append(hook_entry)

with open(settings_path, "w") as f:
    json.dump(settings, f, indent=2)
print(f"Updated {settings_path}")
EOF

# Append memory instruction to CLAUDE.md if not already present
MEMORY_INSTRUCTION='## Memory System

This project uses a semantic memory system (ChromaDB) that captures past architectural decisions and domain facts across sessions. Relevant memories are automatically injected at the start of each turn.

When you see injected memories, use `search_memories` to explore related angles before starting work — implied components, synonymous terms, or connected decisions the automatic query may have missed.'

if [ ! -f "$CLAUDE_MD" ] || ! grep -q "Memory System" "$CLAUDE_MD"; then
    printf "\n%s\n" "$MEMORY_INSTRUCTION" >> "$CLAUDE_MD"
    echo "Updated $CLAUDE_MD"
else
    echo "Memory System instruction already present in $CLAUDE_MD"
fi

echo "Done. Restart Claude Code to activate the hook."
