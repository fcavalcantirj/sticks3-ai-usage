#!/bin/sh
# ota_auto.sh — build once, then upload the moment the device is awake.
#
# The stick sleeps ~19 s after a wake on battery, which is far too short to
# build in.  So: build first (no device needed), then poll and fire.
#
# The device address is read from the DAEMON on every pass, never cached:
# GET /v1/device returns the peer address the stick actually connected from.
# A hardcoded IP silently rots — on 2026-09-10 the device took a new DHCP
# lease after an NVS erase and an hour was lost firing at the old address.
#
# Usage: sh firmware/scripts/ota_auto.sh [timeout_seconds]
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(dirname "$(dirname "$here")")
port=${USAGED_PORT_LOCAL:-8765}
deadline=$(( $(date +%s) + ${1:-1800} ))

echo "== building (device not required) =="
sh "$here/upload_ota.sh" --build-only
bin="$root/firmware/.pio/build/${PIO_ENV:-m5stack-sticks3}/firmware.bin"
[ -f "$bin" ] || { echo "build produced no $bin" >&2; exit 1; }

echo "== waiting for the device — press a button if it is on battery =="
while [ "$(date +%s)" -lt "$deadline" ]; do
    ip=$(curl -s --max-time 2 "http://127.0.0.1:${port}/v1/device" 2>/dev/null \
         | sed -n 's/.*"addr"[[:space:]]*:[[:space:]]*"\([0-9.]*\)".*/\1/p')
    if [ -n "$ip" ] && ping -c1 -W200 "$ip" >/dev/null 2>&1; then
        echo "== device awake at $ip — uploading =="
        exec sh "$here/upload_ota.sh" --upload-only "$bin"
    fi
    sleep 0.2
done
echo "timed out waiting for the device" >&2
exit 1
