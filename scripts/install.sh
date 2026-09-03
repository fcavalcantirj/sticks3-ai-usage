#!/bin/bash
# scripts/install.sh — build, validate config, and install the usaged LaunchAgent.
# Does NOT start live polling against real credentials (that is task 22).
set -euo pipefail

cd "$(dirname "$0")/.."

REPO_DIR="$(pwd)"
PLIST_NAME="com.fcavalcanti.usaged"
PLIST_SRC="launchd/${PLIST_NAME}.plist"
PLIST_DST="$HOME/Library/LaunchAgents/${PLIST_NAME}.plist"
LOG_DIR="$HOME/Library/Logs/usaged"
GID=$(id -u)

# --- Build ---
make build

# --- Validate config before installing ---
if [ ! -f .env ]; then
    echo "FAIL: .env not found."
    echo "Fix: cp .env.example .env && edit USAGED_DEVICE_TOKEN and USAGED_LISTEN"
    exit 1
fi

# Source .env to validate settings.
set -a
source .env
set +a

TOKEN="${USAGED_DEVICE_TOKEN:-}"
LISTEN="${USAGED_LISTEN:-0.0.0.0:8765}"

# Refuse to install on a non-loopback listen with a default/empty token.
case "$TOKEN" in
    ""|change-me-32-chars)
        case "$LISTEN" in
            127.*|0.0.0.0|::1|localhost)
                ;;
            *)
                echo "FAIL: USAGED_DEVICE_TOKEN is empty or 'change-me-32-chars' and USAGED_LISTEN is not loopback."
                echo "Fix: edit .env and set a real USAGED_DEVICE_TOKEN (or bind to 127.0.0.1)."
                exit 1
                ;;
        esac
        ;;
esac

# --- Prepare directories ---
mkdir -p "$LOG_DIR"

# --- Defensive cleanup: remove the temporary com.fcavalcanti.usaged-once job
#     (ORDER #6). This job was created during task 22 as a one-shot; it must
#     not survive a real install. Never bootout com.fcavalcanti.usaged itself
#     — Felipe's status line and dashboard depend on it.
USAGED_ONCE_PLIST="$HOME/Library/LaunchAgents/com.fcavalcanti.usaged-once.plist"
launchctl bootout "gui/${GID}/com.fcavalcanti.usaged-once" 2>/dev/null || true
rm -f "$USAGED_ONCE_PLIST"

# --- Install LaunchAgent ---
echo "Installing LaunchAgent..."
launchctl bootout "gui/${GID}/${PLIST_NAME}" 2>/dev/null || true
cp "$REPO_DIR/$PLIST_SRC" "$PLIST_DST"
launchctl bootstrap "gui/${GID}" "$PLIST_DST"
launchctl kickstart -k "gui/${GID}/${PLIST_NAME}" 2>/dev/null || true

# --- Wait for /healthz ---
HOST=$(echo "$LISTEN" | cut -d: -f1)
PORT=$(echo "$LISTEN" | cut -d: -f2)
BASE="http://127.0.0.1:${PORT}"
if [ "$HOST" = "0.0.0.0" ] || [ "$HOST" = "::0" ]; then
    BASE="http://127.0.0.1:${PORT}"
fi

echo "Waiting for /healthz..."
for _ in $(seq 1 40); do
    if curl -sf "${BASE}/healthz" >/dev/null 2>&1; then
        echo "OK: usaged is running at ${BASE}"
        exit 0
    fi
    sleep 0.5
done

echo "FAIL: usaged did not become healthy on ${BASE} within 20s"
echo "Check logs: tail -f ${LOG_DIR}/usaged.err.log"
exit 1
