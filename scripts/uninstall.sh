#!/bin/bash
# scripts/uninstall.sh — stop and remove the usaged LaunchAgent.
# Preserves log files and state.
set -euo pipefail

cd "$(dirname "$0")/.."

PLIST_NAME="com.fcavalcanti.usaged"
PLIST_DST="$HOME/Library/LaunchAgents/${PLIST_NAME}.plist"
GID=$(id -u)

echo "Stopping LaunchAgent..."
launchctl bootout "gui/${GID}/${PLIST_NAME}" 2>/dev/null || true
# Also try the older `unload` path for resilience.
launchctl unload -w "$PLIST_DST" 2>/dev/null || true

rm -f "$PLIST_DST"

echo "Removed ${PLIST_DST}"
echo "Logs preserved at: $HOME/Library/Logs/usaged/"
echo "State preserved at: $(grep USAGED_STATE .env 2>/dev/null || echo 'see .env')"
