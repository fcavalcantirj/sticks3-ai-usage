#!/bin/bash
# statusline.test.sh — the status line is a shipped surface, and nothing tested it.
#
# WHY THIS EXISTS. When the project was renamed from `usaged` to `ai-usage` the
# state file moved with it, but skills/ai-usage/scripts/statusline.sh kept
# defaulting to $HOME/.local/state/usaged/state.json. That path stopped
# existing, so the bar printed "usaged: not running" on every prompt for days
# while the daemon was healthy and serving. Every Go test stayed green
# throughout: nothing in the suite ever executed the shell script a user
# actually sees.
#
# Run: bash scripts/statusline.test.sh   (wired into `make verify`)
set -uo pipefail
cd "$(dirname "$0")/.."
SCRIPT="skills/ai-usage/scripts/statusline.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM
fails=0
check() { # name, haystack, needle
    if printf '%s' "$2" | grep -qF -- "$3"; then echo "  ok   $1"; else
        echo "  FAIL $1 — expected to find: $3"; echo "       got: $2"; fails=$((fails + 1)); fi
}
refute() {
    if printf '%s' "$2" | grep -qF -- "$3"; then
        echo "  FAIL $1 — must NOT contain: $3"; echo "       got: $2"; fails=$((fails + 1))
    else echo "  ok   $1"; fi
}

# A state file shaped exactly like the daemon's: providers[].id + rows[].k/.pct.
cat > "$TMP/state.json" <<'JSON'
{"snapshot":{"checked_at":9999999999,"providers":[
  {"id":"claude","rows":[{"k":"5h","pct":41},{"k":"7d","pct":62}]},
  {"id":"codex","rows":[{"k":"5h","pct":7},{"k":"7d","pct":88}]}]}}
JSON

echo "== the gauge renders from a state file =="
OUT=$(USAGED_STATE="$TMP/state.json" bash "$SCRIPT" < /dev/null)
check   "claude 5h"          "$OUT" "41%"
check   "claude 7d"          "$OUT" "62%"
check   "codex 5h"           "$OUT" "7%"
check   "codex 7d"           "$OUT" "88%"
refute  "no false down-report" "$OUT" "not running"

echo "== a genuinely missing state file still says so =="
OUT=$(USAGED_STATE="$TMP/nope.json" bash "$SCRIPT" < /dev/null)
check   "reports not running" "$OUT" "not running"
check   "names ai-usage"      "$OUT" "ai-usage: not running"

echo "== the DEFAULT path is the post-rename one =="
# THE REGRESSION ITSELF. With no USAGED_STATE, the script must look under
# .../state/ai-usage/. Run it with HOME pointed at a fixture tree holding ONLY
# the new path: if the default is wrong, it finds nothing and says "not running".
mkdir -p "$TMP/home/.local/state/ai-usage"
cp "$TMP/state.json" "$TMP/home/.local/state/ai-usage/state.json"
OUT=$(env -u USAGED_STATE HOME="$TMP/home" bash "$SCRIPT" < /dev/null)
refute  "default resolves"   "$OUT" "not running"
check   "default reads it"   "$OUT" "41%"

echo "== the pre-rename path still works for an old install =="
mkdir -p "$TMP/old/.local/state/usaged"
cp "$TMP/state.json" "$TMP/old/.local/state/usaged/state.json"
OUT=$(env -u USAGED_STATE HOME="$TMP/old" bash "$SCRIPT" < /dev/null)
refute  "legacy fallback"    "$OUT" "not running"

echo "== stdin rate_limits win over the state file =="
OUT=$(printf '%s' '{"model":{"display_name":"Opus"},"context_window":{"used_percentage":12},"rate_limits":{"five_hour":{"used_percentage":3},"seven_day":{"used_percentage":4}}}' \
      | USAGED_STATE="$TMP/state.json" bash "$SCRIPT")
check   "stdin claude 5h"    "$OUT" "3%"
check   "ctx from stdin"     "$OUT" "12%"
check   "model name"         "$OUT" "Opus"

if [ "$fails" -ne 0 ]; then echo; echo "statusline: $fails check(s) failed"; exit 1; fi
echo; echo "statusline: all checks passed"
