#!/bin/bash
# Open the ai-usage web dashboard in the default browser.
# Loopback needs no token; the LAN URL does, so it is only printed.
set -uo pipefail
PORT="${USAGED_PORT:-8765}"
URL="http://127.0.0.1:${PORT}/"

if ! curl -s -o /dev/null --max-time 3 "http://127.0.0.1:${PORT}/healthz"; then
  echo "ai-usage is not answering on 127.0.0.1:${PORT}."
  echo "Start it:  launchctl kickstart -k gui/\$(id -u)/com.fcavalcanti.ai-usage"
  echo "Or check:  launchctl print gui/\$(id -u)/com.fcavalcanti.ai-usage | grep 'state ='"
  exit 1
fi

SEQ=$(curl -s --max-time 3 "http://127.0.0.1:${PORT}/healthz" | sed -E 's/.*"seq":([0-9]+).*/\1/')
AGE=$(curl -s --max-time 3 "http://127.0.0.1:${PORT}/healthz" | python3 -c "import json,sys,time; d=json.load(sys.stdin); print(int(time.time()-d['checked_at']))" 2>/dev/null || echo "?")
echo "ai-usage is up (seq ${SEQ}, data checked ${AGE}s ago) — opening ${URL}"
open "$URL"

REPO="$HOME/dev/m5/sticks3-ai-usage"
if [ -f "$REPO/.env" ]; then
  TOK=$(grep -E '^USAGED_DEVICE_TOKEN=' "$REPO/.env" | cut -d= -f2-)
  IP=$(ipconfig getifaddr en0 2>/dev/null)
  [ -n "${IP:-}" ] && [ -n "${TOK:-}" ] && echo "From another device on the LAN: http://${IP}:${PORT}/?token=${TOK}"
fi
