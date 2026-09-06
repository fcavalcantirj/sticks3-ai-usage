#!/bin/sh
# OTA build and upload to the StickS3.
#
# Task 69 split the original combined script into phases so a battery flash
# fits the ~20 s awake window: the build (~25 s) runs while USB-powered; the
# upload (~5 s) runs on Felipe's button press while the device is awake on
# battery.
#
# Usage:
#   upload_ota.sh --build-only                          # build phase only
#   upload_ota.sh --upload-only <firmware.bin> [build_id]  # upload phase only
#   upload_ota.sh                                     # build then upload (cable)
#
# The binary path is required for --upload-only; the build ID is optional
# (only echoed for log correlation).  The target is MAC-verified twice: once
# before the build (fail fast) and once immediately before the upload,
# because a multi-minute build can outlive a DHCP lease and DHCP may
# reassign the address.  The upload phase also refuses a missing or stale
# binary — one newer than any source file — so a stale binary can never be
# flashed silently.
#
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
ota_port=3232

# --- MAC guard ---------------------------------------------------------------
# Resolves the host, reads the ARP table, and confirms the MAC matches.
# Prints the resolved IP on stdout so callers can use it.

verify_target() {
    ota_ip=$(ping -c 1 -W 2000 "$ota_host" 2>/dev/null | awk -F'[()]' '/PING/ {print $2; exit}')
    if [ -z "$ota_ip" ]; then
        echo "$ota_host did not resolve or is unreachable" >&2
        exit 1
    fi

    actual_mac=$(arp -n "$ota_ip" 2>/dev/null | awk '/ at / {print $4; exit}')
    actual_mac=$(printf '%s' "$actual_mac" | tr '[:upper:]' '[:lower:]')
    if [ "$actual_mac" != "$expected_mac" ]; then
        echo "Refusing: $ota_host resolved to $actual_mac, expected $expected_mac" >&2
        exit 1
    fi

    printf '%s\n' "$ota_ip"
}

# --- secrets -----------------------------------------------------------------

load_ota_password() {
    if [ ! -f "$secrets_file" ]; then
        echo "secrets.h missing — copy include/secrets.h.example and set OTA_PASS" >&2
        exit 1
    fi

    ota_password=$(awk '/^[[:space:]]*#define[[:space:]]+OTA_PASS[[:space:]]+/ {gsub(/\"/, "", $3); print $3; exit}' "$secrets_file")
    if [ -z "$ota_password" ]; then
        echo "OTA_PASS is missing from secrets.h — set a unique password for THIS board" >&2
        exit 1
    fi
    export OTA_PASS="$ota_password"
}

# --- ESPota tools ------------------------------------------------------------

get_espota_paths() {
    core_json=$("$pio_bin" system info --json-output 2>/dev/null)
    core_dir=$(printf '%s\n' "$core_json" | sed -E 's/.*"core_dir": \{"title": "[^"]+", "value": "([^"]+)".*/\1/')
    espota="$core_dir/packages/framework-arduinoespressif32/tools/espota.py"
    pio_python="$core_dir/penv/bin/python"

    if [ ! -f "$espota" ] || [ ! -x "$pio_python" ]; then
        echo "PlatformIO OTA tools (espota.py / python) are missing" >&2
        exit 1
    fi
}

# --- port poll ---------------------------------------------------------------
# Polls the OTA port so the upload starts the instant the device answers
# instead of depending on human timing.  Uses pio's Python (already required
# for espota) so no extra dependency is needed.

wait_for_ota_port() {
    ota_ip="$1"
    echo "waiting for device — press a button" >&2
    for _ in 1 2 3 4 5 6 7 8 9 10; do
        if "$pio_python" -c \
            "import socket,sys; s=socket.socket(); s.settimeout(1); s.connect(('$ota_ip', $ota_port)); s.close()" \
            2>/dev/null; then
            echo "device is listening on port $ota_port" >&2
            return 0
        fi
        sleep 1
    done
    echo "Refusing: device not listening on port $ota_port after 10s" >&2
    echo "  is the device awake on Wi-Fi? (the screen lights when it is ready)" >&2
    exit 1
}

# --- staleness check ---------------------------------------------------------

check_stale() {
    if [ ! -f "$firmware_path" ]; then
        echo "Refusing: firmware binary not found at '$firmware_path'" >&2
        echo "  run 'upload_ota.sh --build-only' first" >&2
        exit 1
    fi

    # Refuse if any source file is newer than the binary.
    stale=$(find "$firmware_dir/src" "$firmware_dir/include" -type f \
        \( -name '*.cpp' -o -name '*.h' -o -name '*.ino' \) \
        -newer "$firmware_path" 2>/dev/null | head -1)
    if [ -n "$stale" ]; then
        echo "Refusing: firmware binary is stale — '$stale' is newer than the binary" >&2
        echo "  run 'upload_ota.sh --build-only' first" >&2
        exit 1
    fi
}

# --- build phase -------------------------------------------------------------

do_build() {
    echo "OTA env=$pio_env host=$ota_host mac=$expected_mac"
    verify_target > /dev/null  # pre-build MAC check: fail fast if wrong device

    cd "$firmware_dir"

    # Remove cached link products so the build relinks every time (we want
    # the binary we upload to be fresh from this source tree).
    rm -f ".pio/build/$pio_env/firmware.elf" ".pio/build/$pio_env/firmware.bin"

    build_log_file=$(mktemp)
    trap 'rm -f "$build_log_file" "${build_log_file}.status"' EXIT

    ( set +e; "$pio_bin" run -e "$pio_env" 2>&1; echo "$?" > "${build_log_file}.status" ) | tee "$build_log_file"
    build_status=$(cat "${build_log_file}.status")
    if [ "$build_status" != "0" ]; then
        echo "Refusing: build failed" >&2
        exit 1
    fi

    build_id=$(grep -o 'USAGED_BUILD_ID=[^ ]*' "$build_log_file" | tail -1 | cut -d= -f2)
    if [ -z "$build_id" ]; then
        echo "Could not read build id from the build output" >&2
        exit 1
    fi

    firmware=".pio/build/$pio_env/firmware.bin"
    if [ ! -f "$firmware" ]; then
        echo "Refusing: $firmware not produced by the build" >&2
        exit 1
    fi

    firmware_path="$firmware_dir/$firmware"

    # Emit machine-readable output for the upload phase.
    echo "build=$build_id"
    echo "firmware=$firmware_path"
    echo "OTA_BUILD_ID=$build_id"
    echo "OTA_FIRMWARE=$firmware_path"
}

# --- upload phase ------------------------------------------------------------

do_upload() {
    check_stale
    ota_ip=$(verify_target)  # pre-upload MAC check: re-verify right before flashing
    get_espota_paths
    load_ota_password
    wait_for_ota_port "$ota_ip"

    echo "uploading build=$build_id to $ota_ip (MAC guard: $expected_mac)" >&2
    "$pio_python" "$espota" -i "$ota_ip" -p "$ota_port" \
        --auth="$ota_password" \
        -f "$firmware_path"

    echo "OTA upload complete"
}

# --- argument parsing --------------------------------------------------------

mode="both"
build_id="unknown"
firmware_path=""

if [ $# -gt 0 ]; then
    case "$1" in
        --build-only)
            mode="build"
            ;;
        --upload-only)
            mode="upload"
            shift
            if [ $# -lt 1 ]; then
                echo "usage: $0 --upload-only <firmware.bin> [build_id]" >&2
                exit 1
            fi
            firmware_path=$(cd "$(dirname -- "$1")" && pwd)/$(basename -- "$1")
            shift
            if [ $# -ge 1 ]; then
                build_id="$1"
                shift
            fi
            ;;
        *)
            # No recognised flag: combined build+upload (cable sessions).
            mode="both"
            ;;
    esac
fi

case "$mode" in
    build)
        do_build
        ;;
    upload)
        do_upload
        ;;
    both)
        do_build
        # firmware_path and build_id are set by do_build at script scope.
        do_upload
        ;;
esac
