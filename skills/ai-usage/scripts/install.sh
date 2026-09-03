#!/bin/bash
# Point Claude Code's status line at the ai-usage gauge (backs up settings.json first).
set -euo pipefail
SKILL_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SETTINGS="$HOME/.claude/settings.json"
BACKUPS="$HOME/.claude/backups"; mkdir -p "$BACKUPS"
cp "$SETTINGS" "$BACKUPS/settings.json.$(date +%Y%m%d-%H%M%S)"
chmod +x "$SKILL_DIR/scripts/"*.sh
python3 - "$SETTINGS" "$SKILL_DIR/scripts/statusline.sh" <<'EOF'
import json, sys
path, script = sys.argv[1], sys.argv[2]
s = json.load(open(path))
s["statusLine"] = {"type": "command", "command": f'bash "{script}"', "refreshInterval": 60}
json.dump(s, open(path, "w"), indent=2); open(path, "a").write("\n")
print("statusLine ->", s["statusLine"]["command"], "(refresh 60 s)")
EOF
echo '{"model":{"display_name":"Test"},"workspace":{"current_dir":"'"$PWD"'"},"context_window":{"used_percentage":12},"rate_limits":{"five_hour":{"used_percentage":50},"seven_day":{"used_percentage":57}}}' \
  | bash "$SKILL_DIR/scripts/statusline.sh"
echo "installed — new Claude Code sessions show the gauge; the running one picks it up on the next status refresh."
