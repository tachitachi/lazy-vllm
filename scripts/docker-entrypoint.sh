#!/usr/bin/env bash
set -e

# Inject the RTK hook into Claude Code's settings idempotently.
# rtk init -g claims success but doesn't write the file in this environment.
python3 - <<'EOF'
import json, os

path = os.path.expanduser("~/.claude/settings.json")

try:
    with open(path) as f:
        settings = json.load(f)
except (FileNotFoundError, json.JSONDecodeError):
    settings = {}

rtk_hook = {"type": "command", "command": "rtk hook claude"}
pre_tool = settings.setdefault("hooks", {}).setdefault("PreToolUse", [])

bash_entry = next((e for e in pre_tool if e.get("matcher") == "Bash"), None)
if bash_entry is None:
    pre_tool.append({"matcher": "Bash", "hooks": [rtk_hook]})
elif not any(h.get("command") == "rtk hook claude" for h in bash_entry.get("hooks", [])):
    bash_entry.setdefault("hooks", []).append(rtk_hook)

os.makedirs(os.path.dirname(path), exist_ok=True)
with open(path, "w") as f:
    json.dump(settings, f, indent=2)
EOF

# When $@ is just "claude" (the default CMD), don't pass it — that would run `claude claude`.
# When $@ is empty or contains real args, pass it through.
if [[ $# -eq 1 && "$1" == "claude" ]]; then
    exec claude
else
    exec claude "$@"
fi
