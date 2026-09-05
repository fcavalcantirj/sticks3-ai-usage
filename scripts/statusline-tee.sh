#!/usr/bin/env bash
# statusline-tee.sh — Claude Code statusline shim.
#
# Reads Claude Code's statusline stdin JSON, atomically writes the rate_limits
# block to ~/.local/state/usaged/claude-statusline.json, then execs the
# existing statusline hook so the real output is preserved.
#
# Configured in ~/.claude/settings.json by the founder:
#   "hooks": {
#     "statusline": [
#       {
#         "type": "command",
#         "command": "bash /path/to/usaged/scripts/statusline-tee.sh"
#       }
#     ]
#   }
#
# The founder wires this into settings.json; this script only reads stdin and
# writes a file. It never edits ~/.claude/settings.json.

set -euo pipefail

STATE_DIR="${HOME}/.local/state/usaged"
OUTPUT_FILE="${STATE_DIR}/claude-statusline.json"
REAL_HOOK="${HOME}/.claude/hooks/gsd-statusline.js"

mkdir -p "$STATE_DIR"

# Read the full stdin (Claude Code sends one JSON object on stdin).
stdin="$(cat)"

# Extract rate_limits + plan_type using python3 (stdlib only).
printf '%s' "$stdin" | python3 -c '
import json, os, sys, tempfile

data = json.load(sys.stdin)
out = {}
if "rate_limits" in data:
    out["rate_limits"] = data["rate_limits"]
if "plan_type" in data:
    out["plan_type"] = data["plan_type"]

# Write atomically: temp file + rename.
fd, tmp = tempfile.mkstemp(dir=os.environ.get("STATE_DIR", "."), prefix=".cls.")
with os.fdopen(fd, "w") as f:
    json.dump(out, f)
os.replace(tmp, os.path.join(os.environ.get("STATE_DIR", "."), "claude-statusline.json"))
' STATE_DIR="$STATE_DIR"

# Chain to the real statusline hook, passing through the original stdin.
if [ -x "$REAL_HOOK" ]; then
    printf '%s' "$stdin" | exec "$REAL_HOOK"
elif [ -f "$REAL_HOOK" ]; then
    printf '%s' "$stdin" | exec node "$REAL_HOOK"
else
    # No real hook configured — just exit 0.
    exit 0
fi
