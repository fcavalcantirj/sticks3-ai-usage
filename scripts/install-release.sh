#!/bin/bash
# install.sh — install usaged from a downloaded release.
#
# THIS IS NOT scripts/install.sh. That one builds from a git checkout, reads a
# .env the developer filled in, and installs a LaunchAgent whose paths are
# baked to one machine. This one assumes NONE of that: no repo, no Go, no .env,
# no editing. It is what someone who flashed a StickS3 from M5Burner runs.
#
# What it does:
#   1. clears the quarantine attribute (see NOTE below)
#   2. copies the binary to ~/.local/bin/usaged
#   3. writes a LaunchAgent generated for THIS user's home directory
#   4. starts it and waits for /healthz
#
# NOTE ON QUARANTINE. A browser marks every download com.apple.quarantine, and
# macOS then refuses to run an unsigned binary — the "cannot be opened because
# the developer cannot be verified" dialog. Notarizing it would require a paid
# Apple Developer Program certificate, which this project does not have, so the
# honest options are: clear the attribute (what this does, on a file you just
# chose to download), or right-click → Open in Finder. Both are you deciding to
# trust it. Read this script first if you would rather not take that on faith.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
BIN_SRC="${HERE}/ai-usage"
BIN_DIR="${HOME}/.local/bin"
BIN_DST="${BIN_DIR}/ai-usage"
LABEL="com.fcavalcanti.ai-usage"
PLIST="${HOME}/Library/LaunchAgents/${LABEL}.plist"
LOG_DIR="${HOME}/Library/Logs/ai-usage"
CONFIG_DIR="${HOME}/.config/ai-usage"
GID="$(id -u)"
PORT="${USAGED_PORT:-8765}"
TOKEN_FILE="${CONFIG_DIR}/device-token"   # derived, never a second copy of the path

[ -f "$BIN_SRC" ] || { echo "FAIL: ai-usage binary not found next to this script"; exit 1; }

case "$(uname -s)" in
  Darwin) ;;
  *) echo "FAIL: this release is macOS only."
     echo "      The daemon reads Claude's token from the macOS Keychain and drives"
     echo "      CoreBluetooth for device setup; neither exists on this platform."
     exit 1 ;;
esac

echo "== ai-usage =="

# 1. Quarantine. Absent when the tarball came from curl rather than a browser.
if xattr -p com.apple.quarantine "$BIN_SRC" >/dev/null 2>&1; then
    echo "-- clearing the download quarantine flag"
    xattr -d com.apple.quarantine "$BIN_SRC" 2>/dev/null || true
fi

# 2. Migrate from the old name.
#
# This project shipped as "usaged" before it was renamed. An upgrade must not
# leave the old agent running beside the new one, and must not orphan the
# paired-device store — devices.json holds the tokens issued to every stick
# already set up, so losing it means every one of them is refused.
OLD_LABEL="com.fcavalcanti.usaged"
OLD_PLIST="${HOME}/Library/LaunchAgents/${OLD_LABEL}.plist"
OLD_STATE="${HOME}/.local/state/usaged"
NEW_STATE="${HOME}/.local/state/ai-usage"

if [ -f "$OLD_PLIST" ] || [ -x "${BIN_DIR}/usaged" ]; then
    echo "-- migrating from the old \"usaged\" name"
    launchctl bootout "gui/${GID}/${OLD_LABEL}" 2>/dev/null || true
    rm -f "$OLD_PLIST" "${BIN_DIR}/usaged"
fi
if [ -d "$OLD_STATE" ] && [ ! -d "$NEW_STATE" ]; then
    mkdir -p "$(dirname "$NEW_STATE")"
    mv "$OLD_STATE" "$NEW_STATE"
    echo "   kept your paired devices and snapshot"
fi

# 3. Binary.
# LaunchAgents may not exist yet on a Mac that has never had a user agent.
mkdir -p "$BIN_DIR" "$LOG_DIR" "$CONFIG_DIR" "$(dirname "$PLIST")"
launchctl bootout "gui/${GID}/${LABEL}" 2>/dev/null || true
cp "$BIN_SRC" "$BIN_DST"
chmod +x "$BIN_DST"
echo "-- installed ${BIN_DST}"

[ -f "${CONFIG_DIR}/config.yaml" ] || {
    [ -f "${HERE}/config.example.yaml" ] && cp "${HERE}/config.example.yaml" "${CONFIG_DIR}/config.example.yaml"
}

# 4. A device token, generated once and kept.
#
# THE AGENT MUST BIND THE LAN, NOT LOOPBACK, OR THE STICK CANNOT REACH IT.
# That is the whole point of the product: the device fetches over Wi-Fi. An
# earlier version of this installer bound 127.0.0.1 and the setup flow failed
# outright with "the agent listens on loopback only, so the device could never
# reach it" (internal/bleprov/creds.go ErrLoopbackOnly).
#
# Binding the LAN requires a real token — api.New refuses a non-loopback listen
# without one, deliberately, so nobody exposes their usage data to the network
# by accident. So the installer MINTS one instead of asking the user for it.
# They never need to see it: loopback GETs and the /v1/setup routes skip the
# token, so the dashboard and device setup work untouched, and the device is
# given its own token over Bluetooth.
#
# It is generated ONCE and reused on upgrade — regenerating would silently
# orphan every device already provisioned against the old value.
if [ -s "$TOKEN_FILE" ]; then
    TOKEN="$(cat "$TOKEN_FILE")"
    echo "-- reusing the existing device token"
else
    # 16 random bytes as 32 hex chars. NOT `tr -dc ... | head -c 32`: head
    # closes the pipe, tr dies of SIGPIPE, and `set -e` then kills the whole
    # installer after the binary is already copied — which is exactly how this
    # went out broken the first time.
    TOKEN="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
    ( umask 077; printf '%s' "$TOKEN" > "$TOKEN_FILE" )
    echo "-- generated a device token (${TOKEN_FILE})"
fi

# 5. LaunchAgent, generated for THIS home directory.
#
# PATH is spelled out because launchd gives a process almost none, and both
# `security` (the Keychain, for the Claude token) and the claude/codex CLIs must
# resolve.
cat > "$PLIST" <<PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${LABEL}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${BIN_DST}</string>
    <string>serve</string>
  </array>
  <key>WorkingDirectory</key>
  <string>${HOME}</string>
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
    <string>0.0.0.0:${PORT}</string>
    <key>USAGED_DEVICE_TOKEN</key>
    <string>${TOKEN}</string>
  </dict>
</dict>
</plist>
PLIST_EOF
echo "-- wrote ${PLIST}"

# 6. Start.
launchctl bootstrap "gui/${GID}" "$PLIST"
launchctl kickstart -k "gui/${GID}/${LABEL}" 2>/dev/null || true

printf -- "-- waiting for the agent"
for _ in $(seq 1 60); do
    if curl -sf "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
        echo
        echo
        echo "   ai-usage is running:  http://127.0.0.1:${PORT}"
        echo
        echo "   Next:"
        echo "     1. Open that address."
        echo "     2. Claude and ChatGPT quotas appear if you use Claude Code or the"
        echo "        Codex CLI — ai-usage READS the credentials those tools already"
        echo "        store. It never asks you for a password and never refreshes a"
        echo "        token. Providers you do not use just say so."
        echo "     3. To set up an M5StickS3: power it on, open Settings on that page,"
        echo "        and click \"Look for a device\". Everything the stick needs is"
        echo "        sent over Bluetooth — if macOS asks for six digits, they are on"
        echo "        the stick's own screen."
        echo
        # The interface that actually routes, not a guess at "en0" — Wi-Fi is
        # not en0 on every Mac.
        LAN_IF="$(route -n get default 2>/dev/null | awk '/interface:/{print $2}')"
        LAN_IP="$(ipconfig getifaddr "${LAN_IF:-en0}" 2>/dev/null || echo '<this-mac>')"
        echo "   The dashboard is also on your LAN at http://${LAN_IP}:${PORT}"
        echo "   — that is how the stick reaches it. Anything other than this Mac"
        echo "   needs the token in ${TOKEN_FILE}."
        echo
        echo "   Logs:      ${LOG_DIR}/ai-usage.err.log"
        echo "   Uninstall: launchctl bootout gui/${GID}/${LABEL} && rm -f \"${PLIST}\" \"${BIN_DST}\""
        exit 0
    fi
    printf .
    sleep 0.5
done

echo
echo "FAIL: ai-usage did not answer on 127.0.0.1:${PORT} within 30s"
echo "      Logs: ${LOG_DIR}/ai-usage.err.log"
exit 1
