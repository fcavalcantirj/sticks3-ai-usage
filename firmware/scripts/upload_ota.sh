#!/bin/sh
# Build and upload firmware to the StickS3 OTA identity ("sticks3-usage").
#
# The USB cable was attached for task 37; this is the last cable session.
# From here on, all updates go over Wi-Fi via ArduinoOTA.
#
# Safety: the target is verified by MAC address (14:c1:9f:d4:d5:34 = unit #2)
# twice — once before the build and once again immediately before the upload —
# because DHCP can reassign the address during a multi-minute build and we
# must not flash another board.  The OTA password is extracted from the
# git-ignored secrets.h with awk and is never printed.

set -eu

# --- paths -------------------------------------------------------------------

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
firmware_dir=$(dirname "$script_dir")
repo_dir=$(dirname "$firmware_dir")

# --- config ------------------------------------------------------------------

pio_bin=${PLATFORMIO_CLI_BIN:-platformio}
pio_env=${PIO_ENV:-m5stack-sticks3}
ota_host=${OTA_HOST:-sticks3-usage.local}
secrets_file="$firmware_dir/include/secrets.h"
expected_mac="14:c1:9f:d4:d5:34"

if [ ! -f "$secrets_file" ]; then
    echo "secrets.h missing — copy include/secrets.h.example and set OTA_PASS" >&2
    exit 1
fi

# Extract OTA_PASS without ever echoing it.
ota_password=$(awk '/^[[:space:]]*#define[[:space:]]+OTA_PASS[[:space:]]+/ {gsub(/\"/, "", $3); print $3; exit}' "$secrets_file")
if [ -z "$ota_password" ]; then
    echo "OTA_PASS is missing from secrets.h — set a unique password for THIS board" >&2
    exit 1
fi
export OTA_PASS="$ota_password"

# --- MAC-guarded target verification ----------------------------------------

# Ping the hostname to resolve its IP, then read the ARP table to confirm the
# MAC matches our device.  Run before the build (fail fast) and again right
# before the upload (because a multi-minute build can outlive a DHCP lease).
verify_target() {
    ota_ip=$(ping -c 1 -W 2000 "$ota_host" 2>/dev/null | awk -F'[()]' '/PING/ {print $2; exit}')
    if [ -z "$ota_ip" ]; then
        echo "$ota_host did not resolve or is unreachable; is the device flashed and on Wi-Fi?" >&2
        exit 1
    fi

    actual_mac=$(arp -n "$ota_ip" 2>/dev/null | awk '/ at / {print $4; exit}')
    actual_mac=$(printf '%s' "$actual_mac" | tr '[:upper:]' '[:lower:]')
    if [ "$actual_mac" != "$expected_mac" ]; then
        echo "Refusing OTA: $ota_host resolved to $actual_mac, expected $expected_mac" >&2
        exit 1
    fi
}

echo "OTA env=$pio_env host=$ota_host mac=$expected_mac"

# --- pre-build guard --------------------------------------------------------

verify_target

# --- build ------------------------------------------------------------------

cd "$firmware_dir"

# Remove cached link products so the build relinks every time (we want the
# binary we upload to be fresh from this source tree).
rm -f ".pio/build/$pio_env/firmware.elf" ".pio/build/$pio_env/firmware.bin"

build_log_file=$(mktemp)
trap 'rm -f "$build_log_file"' EXIT

( set +e; "$pio_bin" run -e "$pio_env" 2>&1; echo "$?" > "${build_log_file}.status" ) | tee "$build_log_file"
build_status=$(cat "${build_log_file}.status")
if [ "$build_status" != "0" ]; then
    echo "Refusing OTA: build failed" >&2
    exit 1
fi

build_id=$(grep -o 'USAGED_BUILD_ID=[^ ]*' "$build_log_file" | tail -1 | cut -d= -f2)
if [ -z "$build_id" ]; then
    echo "Could not read build id from the build output" >&2
    exit 1
fi

firmware=".pio/build/$pio_env/firmware.bin"
if [ ! -f "$firmware" ]; then
    echo "Refusing OTA: $firmware not produced by the build" >&2
    exit 1
fi

echo "build=$build_id"

# --- pre-upload guard -------------------------------------------------------

verify_target

# --- upload -----------------------------------------------------------------

# PlatformIO's `pio run -t upload` cannot take an --auth flag on the command
# line (no --project-option in v6.1.19), and putting upload_flags in
# platformio.ini would break serial uploads.  So we drive espota.py directly
# with pio's own Python — the same toolchain pio uses for OTA uploads.  The
# password is passed on the argv and never echoed.
core_json=$("$pio_bin" system info --json-output 2>/dev/null)
core_dir=$(printf '%s\n' "$core_json" | sed -E 's/.*"core_dir": \{"title": "[^"]+", "value": "([^"]+)".*/\1/')
espota="$core_dir/packages/framework-arduinoespressif32/tools/espota.py"
pio_python="$core_dir/penv/bin/python"

if [ ! -f "$espota" ] || [ ! -x "$pio_python" ]; then
    echo "PlatformIO OTA tools (espota.py / penx python) are missing" >&2
    exit 1
fi

echo "uploading build=$build_id to $ota_host ($ota_ip)"
"$pio_python" "$espota" -i "$ota_ip" -p 3232 \
    --auth="$ota_password" \
    -f "$firmware"

echo "OTA upload complete"
