#!/bin/bash
# Remove the ai-usage status line from Claude Code settings (keeps a backup).
set -euo pipefail
SETTINGS="$HOME/.claude/settings.json"
BACKUPS="$HOME/.claude/backups"; mkdir -p "$BACKUPS"
cp "$SETTINGS" "$BACKUPS/settings.json.$(date +%Y%m%d-%H%M%S)"
python3 - "$SETTINGS" <<'EOF'
import json, sys
path = sys.argv[1]; s = json.load(open(path))
sl = s.get("statusLine") or {}
if "ai-usage/scripts/statusline.sh" in sl.get("command", ""):
    del s["statusLine"]; print("statusLine removed")
else:
    print("statusLine is not ai-usage; left untouched:", sl)
json.dump(s, open(path, "w"), indent=2); open(path, "a").write("\n")
EOF
