#!/bin/sh
# flash_when_awake.sh — build now, upload the moment the device wakes.
#
# The StickS3 sleeps with a 12 h timer backstop (ORDER #60), so on battery it
# is unreachable until someone presses a button, and the awake window is only
# ~19 s. A build takes ~25 s, so build-then-upload can never fit inside one
# window. This script does the slow half up front — with no device needed,
# since upload_ota.sh --build-only no longer requires one — then polls at
# 2 s resolution and uploads within a second or two of the device appearing.
#
# Usage:
#   sh scripts/flash_when_awake.sh [build_id] [poll_seconds]
#
# Both MAC guards and the staleness guard in upload_ota.sh still apply: this
# only removes the human timing problem, never a safety check.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
firmware_dir=$(dirname "$script_dir")
cd "$firmware_dir"

build_id=${1:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}
poll_secs=${2:-1800}
ota_host=${OTA_HOST:-sticks3-usage.local}
bin=".pio/build/${PIO_ENV:-m5stack-sticks3}/firmware.bin"

echo "== building $build_id (device not required) =="
sh "$script_dir/upload_ota.sh" --build-only

# Resolve the host once so the poll costs nothing per iteration. If it is
# asleep mDNS may not answer, so fall back to the last known address.
ota_ip=$(ping -c 1 -W 2000 "$ota_host" 2>/dev/null | awk -F'[()]' '/PING/ {print $2; exit}')
[ -z "$ota_ip" ] && ota_ip=${OTA_IP:-192.168.0.136}

echo "== waiting up to ${poll_secs}s for $ota_ip — press a button on the device =="
elapsed=0
while [ "$elapsed" -lt "$poll_secs" ]; do
    if ping -c 1 -W 800 "$ota_ip" >/dev/null 2>&1; then
        echo "awake at $(date +%H:%M:%S) — uploading"
        exec sh "$script_dir/upload_ota.sh" --upload-only "$bin" "$build_id"
    fi
    sleep 2
    elapsed=$((elapsed + 2))
done

echo "device never woke within ${poll_secs}s; the binary is built and ready at $bin" >&2
exit 1
