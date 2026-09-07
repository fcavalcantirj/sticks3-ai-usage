#!/bin/bash
# scripts/install.sh — build, validate config, and install the ai-usage LaunchAgent.
# Does NOT start live polling against real credentials (that is task 22).
set -euo pipefail

cd "$(dirname "$0")/.."

REPO_DIR="$(pwd)"
PLIST_NAME="com.fcavalcanti.ai-usage"
PLIST_SRC="launchd/${PLIST_NAME}.plist"
PLIST_DST="$HOME/Library/LaunchAgents/${PLIST_NAME}.plist"
LOG_DIR="$HOME/Library/Logs/ai-usage"
GID=$(id -u)

# --- Build ---
make build

# --- Config ---
#
# A .env is OPTIONAL. Without one, this behaves like the released installer:
# bind the LAN (the device has to reach us) and mint a device token, so
# installing from source needs no hand-editing either. With one, it wins —
# that is the developer lane, and it is how provider API keys get in.
if [ -f .env ]; then
    set -a
    # shellcheck disable=SC1091
    source .env
    set +a
else
    TOKEN_FILE="$HOME/.config/ai-usage/device-token"
    mkdir -p "$(dirname "$TOKEN_FILE")"
    if [ ! -s "$TOKEN_FILE" ]; then
        ( umask 077; od -An -N16 -tx1 /dev/urandom | tr -d ' \n' > "$TOKEN_FILE" )
        echo "No .env — generated a device token at $TOKEN_FILE"
    fi
    export USAGED_DEVICE_TOKEN="$(cat "$TOKEN_FILE")"
    export USAGED_LISTEN="${USAGED_LISTEN:-0.0.0.0:8765}"
fi

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

# --- Defensive cleanup: remove the temporary com.fcavalcanti.ai-usage-once job
#     (ORDER #6). This job was created during task 22 as a one-shot; it must
#     not survive a real install. Never bootout com.fcavalcanti.ai-usage itself
#     — Felipe's status line and dashboard depend on it.
USAGED_ONCE_PLIST="$HOME/Library/LaunchAgents/com.fcavalcanti.ai-usage-once.plist"
launchctl bootout "gui/${GID}/com.fcavalcanti.ai-usage-once" 2>/dev/null || true
rm -f "$USAGED_ONCE_PLIST"

# --- Install LaunchAgent ---
echo "Installing LaunchAgent..."
launchctl bootout "gui/${GID}/${PLIST_NAME}" 2>/dev/null || true
# Also clear the pre-rename agent, so an upgrade does not leave two running.
launchctl bootout "gui/${GID}/com.fcavalcanti.usaged" 2>/dev/null || true
rm -f "$HOME/Library/LaunchAgents/com.fcavalcanti.usaged.plist"

# GENERATED, not copied. launchd needs absolute paths and the checked-in plist
# cannot know where someone cloned this or who they are — a copied plist works
# on exactly one machine, which is the bug this whole file exists to avoid.
mkdir -p "$(dirname "$PLIST_DST")"
cat > "$PLIST_DST" <<PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${PLIST_NAME}</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/bash</string>
    <string>-lc</string>
    <string>exec ${REPO_DIR}/scripts/run.sh</string>
  </array>
  <key>WorkingDirectory</key>
  <string>${REPO_DIR}</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>30</integer>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>${LOG_DIR}/ai-usage.out.log</string>
  <key>StandardErrorPath</key>
  <string>${LOG_DIR}/ai-usage.err.log</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>${HOME}/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    <key>USAGED_LISTEN</key>
    <string>${USAGED_LISTEN:-0.0.0.0:8765}</string>
    <key>USAGED_DEVICE_TOKEN</key>
    <string>${USAGED_DEVICE_TOKEN:-}</string>
  </dict>
</dict>
</plist>
PLIST_EOF
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
        echo "OK: ai-usage is running at ${BASE}"
        exit 0
    fi
    sleep 0.5
done

echo "FAIL: ai-usage did not become healthy on ${BASE} within 20s"
echo "Check logs: tail -f ${LOG_DIR}/ai-usage.err.log"
exit 1
