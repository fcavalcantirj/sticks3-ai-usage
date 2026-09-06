#!/bin/bash
# ai-usage statusline — model · project · ctx gauge · Claude 5h/7d · GPT 5h/7d
#
# Sources (no network, no API calls per render):
#   - Claude 5h/7d: Claude Code's own statusline stdin `rate_limits` (official; present on Pro/Max).
#     Fallback: usaged state file (claude provider rows).
#   - ChatGPT/Codex 5h/7d: usaged state file written by the LaunchAgent
#     ($HOME/.local/state/usaged/state.json, override with USAGED_STATE).
#   - ctx: stdin context_window.used_percentage.
# Env: AI_USAGE_COMPACT=1 drops the bars; AI_USAGE_BAR=N sets bar width (default 8).
set -u
input=$(cat 2>/dev/null || true)
STATE="${USAGED_STATE:-$HOME/.local/state/usaged/state.json}"
W="${AI_USAGE_BAR:-8}"

# One jq pass over stdin → TSV: model, dir, ctx, c5, c7
IFS=$'\t' read -r model dir ctx c5 c7 < <(printf '%s' "$input" | jq -r '
  [ (.model.display_name // "Claude"),
    (.workspace.current_dir // ""),
    (.context_window.used_percentage // "-"),
    (.rate_limits.five_hour.used_percentage // "-"),
    (.rate_limits.seven_day.used_percentage // "-") ] | @tsv' 2>/dev/null || printf 'Claude\t\t-\t-\t-\n')

g5="-"; g7="-"; s5="-"; s7="-"; age="-"
if [ -f "$STATE" ]; then
  IFS=$'\t' read -r g5 g7 s5 s7 age < <(jq -r '
    def row(p; k): first(.snapshot.providers[]? | select(.id == p) | .rows[]? | select(.k == k));
    [ (row("codex";"5h").pct // "-"), (row("codex";"7d").pct // "-"),
      (row("claude";"5h").pct // "-"), (row("claude";"7d").pct // "-"),
      ((now - (.snapshot.checked_at // 0)) / 60 | floor) ] | @tsv' "$STATE" 2>/dev/null || printf -- '-\t-\t-\t-\t-\n')
fi
[ -z "${model:-}" ] && model="Claude"
[ -z "${ctx:-}" ] && ctx="-"
[ -z "${c5:-}" ] || [ "$c5" = "-" ] && c5="$s5"
[ -z "${c7:-}" ] || [ "$c7" = "-" ] && c7="$s7"

pct() { # float/"-" → int or "-"
  case "$1" in ""|"-"|null) echo "-";; *) printf '%.0f' "$1" 2>/dev/null || echo "-";; esac
}
bar() { # int pct → colored bar + pct
  local p; p=$(pct "$1")
  if [ "$p" = "-" ]; then printf '\033[90m%s\033[0m' "n/a"; return; fi
  [ "$p" -lt 0 ] && p=0; [ "$p" -gt 100 ] && p=100
  local c='\033[32m'; [ "$p" -ge 50 ] && c='\033[33m'; [ "$p" -ge 80 ] && c='\033[31m'
  if [ "${AI_USAGE_COMPACT:-0}" = "1" ]; then printf "%b%d%%\033[0m" "$c" "$p"; return; fi
  local f=$(( (p * W + 50) / 100 )) s="" i
  for ((i = 0; i < W; i++)); do if [ "$i" -lt "$f" ]; then s+="█"; else s+="░"; fi; done
  printf "%b%s %d%%\033[0m" "$c" "$s" "$p"
}
plain() { local p; p=$(pct "$1"); if [ "$p" = "-" ]; then printf '\033[90mn/a\033[0m'; else
  local c='\033[32m'; [ "$p" -ge 50 ] && c='\033[33m'; [ "$p" -ge 80 ] && c='\033[31m'; printf "%b%d%%\033[0m" "$c" "$p"; fi; }

proj=$(basename "${dir:-$PWD}")
sep=' \033[90m·\033[0m '
line="\033[1m${model}\033[0m${sep}${proj}"
[ "$ctx" != "-" ] && line+="${sep}ctx $(bar "$ctx")"
line+="${sep}Claude 5h $(bar "$c5") 7d $(plain "$c7")"
line+="${sep}GPT 5h $(bar "$g5") 7d $(plain "$g7")"
if [ ! -f "$STATE" ]; then line+="${sep}\033[90musaged: not running\033[0m"
elif [ "$age" != "-" ] && [ "$age" -gt 45 ] 2>/dev/null; then line+="${sep}\033[33mstale ${age}m\033[0m"; fi
printf '%b\n' "$line"
