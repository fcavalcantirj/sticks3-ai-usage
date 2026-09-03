# 24-Hour Soak Guide

The usaged LaunchAgent (`com.fcavalcanti.usaged`) runs continuously via `keepAlive`
after a successful `make install`. This guide explains how to confirm it is healthy
over a 24-hour window and what to do when it isn't.

## Reading the logs after 24 h

```bash
# Total successful provider polls (both Claude and Codex status ok):
grep -c '"status":"ok"' ~/Library/Logs/usaged/usaged.err.log

# Any 429 (rate-limit back-off) — expect zero or very few:
grep -c '"status":"error".*"429' ~/Library/Logs/usaged/usaged.err.log

# No panics:
grep -c 'panic' ~/Library/Logs/usaged/usaged.err.log
```

A healthy day looks like: the `"status":"ok"` count grows roughly in step with the
900 s poll interval (2 poll cycles × 2 providers = 4 ok-lines per cycle), the 429
count stays at 0, and the panic count is 0.

## Confirming the Keychain prompt did not return after token rotation

Claude Code rotates the `Claude Code-credentials` Keychain item roughly every
8 hours. If the founder clicked **Always Allow** at install time, the item's
modification date (`mdat`) should stay stable across polls:

```bash
# Compare the Keychain item's mdat with the log's last ok time:
security find-generic-password -s "Claude Code-credentials" | grep mdat
# e.g. mdat: Sep  3 18:42:08 2026

grep '"claude".*"status":"ok"' ~/Library/Logs/usaged/usaged.err.log | tail -1
# Check that the log timestamp is AFTER the mdat — if the Keychain prompt
# reappears, the poller will log status "auth" + msg "run claude" instead.
```

If the prompt returns, the poller automatically degrades to `auth` status and
shows a badge in the web UI. No token is ever printed or refreshed by usaged.

## On `auth` status

If `/v1/usage` shows a provider with `"status":"auth"`:

1. **Claude** — run `claude` once in a terminal. The Claude Code CLI refreshes
   the Keychain token on launch. Then either wait for the next 900 s poll or
   trigger one immediately:

   ```bash
   curl -X POST http://127.0.0.1:8765/v1/refresh \
     -H "X-Device-Token: <token from .env>"
   ```

2. **Codex** — run `codex` once so `~/.codex/auth.json` is refreshed. Then
   POST `/v1/refresh` as above.

The poller will recover on the next tick or after the manual refresh.

## Health check one-liner

```bash
curl -s http://127.0.0.1:8765/healthz | python3 -c \
  'import json,sys; d=json.load(sys.stdin); assert d["ok"] and d["seq"]>=1; print("healthy")'
```
