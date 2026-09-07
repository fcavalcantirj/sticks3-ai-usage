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
BIN_SRC="${HERE}/usaged"
BIN_DIR="${HOME}/.local/bin"
BIN_DST="${BIN_DIR}/usaged"
LABEL="com.fcavalcanti.usaged"
PLIST="${HOME}/Library/LaunchAgents/${LABEL}.plist"
LOG_DIR="${HOME}/Library/Logs/usaged"
CONFIG_DIR="${HOME}/.config/usaged"
GID="$(id -u)"
PORT="${USAGED_PORT:-8765}"

[ -f "$BIN_SRC" ] || { echo "FAIL: usaged binary not found next to this script"; exit 1; }

case "$(uname -s)" in
  Darwin) ;;
  *) echo "FAIL: this release is macOS only."
     echo "      The daemon reads Claude's token from the macOS Keychain and drives"
     echo "      CoreBluetooth for device setup; neither exists on this platform."
     exit 1 ;;
esac

echo "== usaged =="

# 1. Quarantine. Absent when the tarball came from curl rather than a browser.
if xattr -p com.apple.quarantine "$BIN_SRC" >/dev/null 2>&1; then
    echo "-- clearing the download quarantine flag"
    xattr -d com.apple.quarantine "$BIN_SRC" 2>/dev/null || true
fi

# 2. Binary.
# LaunchAgents may not exist yet on a Mac that has never had a user agent.
mkdir -p "$BIN_DIR" "$LOG_DIR" "$CONFIG_DIR" "$(dirname "$PLIST")"
launchctl bootout "gui/${GID}/${LABEL}" 2>/dev/null || true
cp "$BIN_SRC" "$BIN_DST"
chmod +x "$BIN_DST"
echo "-- installed ${BIN_DST}"

[ -f "${CONFIG_DIR}/config.yaml" ] || {
    [ -f "${HERE}/config.example.yaml" ] && cp "${HERE}/config.example.yaml" "${CONFIG_DIR}/config.example.yaml"
}

# 3. LaunchAgent, generated for THIS home directory.
#
# It binds 127.0.0.1 and sets NO device token, which is deliberate and is what
# makes a no-configuration install work: loopback GETs and the /v1/setup routes
# need no token, and the agent REFUSES to start on a non-loopback address
# without one. Someone who later wants the dashboard reachable from their phone
# sets USAGED_LISTEN and USAGED_DEVICE_TOKEN in this file.
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
  <string>${LOG_DIR}/usaged.out.log</string>
  <key>StandardErrorPath</key>
  <string>${LOG_DIR}/usaged.err.log</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>${HOME}/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    <key>USAGED_LISTEN</key>
    <string>127.0.0.1:${PORT}</string>
  </dict>
</dict>
</plist>
PLIST_EOF
echo "-- wrote ${PLIST}"

# 4. Start.
launchctl bootstrap "gui/${GID}" "$PLIST"
launchctl kickstart -k "gui/${GID}/${LABEL}" 2>/dev/null || true

printf -- "-- waiting for the agent"
for _ in $(seq 1 60); do
    if curl -sf "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
        echo
        echo
        echo "   usaged is running:  http://127.0.0.1:${PORT}"
        echo
        echo "   Next:"
        echo "     1. Open that address."
        echo "     2. Claude and ChatGPT quotas appear if you use Claude Code or the"
        echo "        Codex CLI — usaged READS the credentials those tools already"
        echo "        store. It never asks you for a password and never refreshes a"
        echo "        token. Providers you do not use just say so."
        echo "     3. To set up an M5StickS3: power it on, open Settings on that page,"
        echo "        and click \"Look for a device\". Everything the stick needs is"
        echo "        sent over Bluetooth — if macOS asks for six digits, they are on"
        echo "        the stick's own screen."
        echo
        echo "   Logs:      ${LOG_DIR}/usaged.err.log"
        echo "   Uninstall: launchctl bootout gui/${GID}/${LABEL} && rm -f \"${PLIST}\" \"${BIN_DST}\""
        exit 0
    fi
    printf .
    sleep 0.5
done

echo
echo "FAIL: usaged did not answer on 127.0.0.1:${PORT} within 30s"
echo "      Logs: ${LOG_DIR}/usaged.err.log"
exit 1
