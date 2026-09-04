---
name: ai-usage
description: AI usage gauge for Felipe — Claude Max 5h/7d and ChatGPT/Codex 5h/7d quotas in the Claude Code status line and on demand. Use when Felipe asks "how much usage/quota is left", "5 hour limit", "weekly limit", "am I rate limited", "usage gauge", "statusline", or anything about the sticks3-ai-usage / usaged project (Go poller at ~/dev/m5/sticks3-ai-usage, StickS3 display). Installs/uninstalls the status line; prints the current table.
argument-hint: [status | webui | refresh | table | install | uninstall]
---

# ai-usage — the gauge on the bottom bar

Replaces the old GSD status line. One bash script, no network per render:

```
Fable · sticks3-ai-usage · ctx ██░░░░░░ 12% · Claude 5h ████░░░░ 50% 7d 57% · GPT 5h ░░░░░░░░ 0% 7d 54% $165.77
```

Sources:
- **Claude 5h/7d**: Claude Code's own status-line stdin `rate_limits.five_hour/seven_day.used_percentage` (official, Max/Pro plans). Fallback: usaged state file.
- **ChatGPT/Codex 5h/7d + credits**: `~/.local/state/usaged/state.json`, written every 15 min by the `usaged` LaunchAgent (`com.fcavalcanti.usaged`, repo `~/dev/m5/sticks3-ai-usage`, `make install`). Until that agent runs, the bar says `usaged: not running`; if the file is older than 45 min it says `stale Nm`.
- **ctx**: stdin `context_window.used_percentage`.

Colors: green < 50 %, yellow 50–79 %, red ≥ 80 %. `AI_USAGE_COMPACT=1` drops the bars, `AI_USAGE_BAR=12` widens them.

## Commands (SKILL_DIR = this directory)

| ask | do |
|---|---|
| status / no args | `bash SKILL_DIR/scripts/statusline.sh < /dev/null` (prints the line from the state file only) and, if the repo exists, `cd ~/dev/m5/sticks3-ai-usage && go run ./cmd/usaged once` for the full table (live, read-only, Keychain + ~/.codex/auth.json; exits 3 when a provider needs `run claude` / `run codex`) |
| webui / web / open / dashboard | `bash SKILL_DIR/scripts/webui.sh` — checks the agent is answering, prints the snapshot age, opens http://127.0.0.1:8765/ in the browser, and prints the LAN URL with the device token for a phone |
| refresh / force | `curl -s -X POST -H "X-Device-Token: $(grep ^USAGED_DEVICE_TOKEN ~/dev/m5/sticks3-ai-usage/.env | cut -d= -f2-)" http://127.0.0.1:8765/v1/refresh` — makes the agent poll the providers now instead of waiting for the 15-minute tick |
| table | `curl -s http://127.0.0.1:8765/v1/usage.txt` when the LaunchAgent is running, else the `once` command above |
| install | `bash SKILL_DIR/scripts/install.sh` — backs up `~/.claude/settings.json`, sets `statusLine` to this script with `refreshInterval: 60` |
| uninstall | `bash SKILL_DIR/scripts/uninstall.sh` — removes only an ai-usage statusLine, keeps a backup |

## Rules

- Never read, print, refresh or export the OAuth tokens; the gauge only consumes percentages. `usaged` reads the Keychain item `Claude Code-credentials` and `~/.codex/auth.json` read-only.
- Never edit `~/.claude/settings.json` by hand for this — use the install/uninstall scripts (they back up first).
- The Claude numbers from stdin are authoritative while a session runs; `usaged` numbers may lag up to 15 min.
- If both `Claude` and `GPT` show `n/a`: `launchctl print gui/$(id -u)/com.fcavalcanti.usaged` and `tail ~/Library/Logs/usaged/usaged.err.log`.

## Related

- Project ledger: `~/dev/m5/sticks3-ai-usage/spec.json` (ralph loop, `ralph-brow` skill). Solvr room `sticks3-ai-usage` steers the builder.
- StickS3 firmware (later tasks in the ledger) shows the same snapshot on the device.
