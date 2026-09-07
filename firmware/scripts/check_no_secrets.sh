#!/bin/sh
# check_no_secrets.sh — refuse a firmware binary that still carries credentials.
#
# Task 76 moves the Wi-Fi SSID, Wi-Fi password, host, device token and OTA
# password out of the compiled image and into NVS, because secrets.h is a
# compile-time header and every one of those values ends up as a plaintext
# string inside firmware.bin — verified with `strings` against a real build.
# Publishing such a binary hands out the network password of whoever built it.
#
# This script is the gate that keeps that from coming back.  It compares the
# binary against the values currently configured in firmware/include/secrets.h
# and fails if any of them is still in there.
#
# Usage:
#   check_no_secrets.sh [path/to/firmware.bin] [path/to/secrets.h]
#
# The second argument exists because the check that actually matters runs
# against a binary built with NO secrets.h — so the values to compare against
# have to come from somewhere else.  publish_check.sh passes the real header
# aside while it builds, then hands the path in here.  Run against a DEVELOPER
# build the values ARE present (secrets.h is compiled in as the first-boot NVS
# seed) and this script correctly fails; that is not the interesting case.
#
# The default binary is firmware/.pio/build/$PIO_ENV/firmware.bin (note: under
# firmware/.pio, not .pio at the repo root).  Paths resolve from the script's
# own location, so it works from any cwd.
#
# ALL FIVE values are checked.  An earlier write-up listed four and omitted
# OTA_PASS, which is exactly how a security check passes while leaking.
#
# The output NEVER contains a credential value — only macro names and a
# verdict — so it is safe to paste anywhere.
#
set -eu

# --- paths -------------------------------------------------------------------

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
firmware_dir=$(dirname "$script_dir")

pio_env=${PIO_ENV:-m5stack-sticks3}
bin=${1:-"$firmware_dir/.pio/build/$pio_env/firmware.bin"}
secrets_file=${2:-"$firmware_dir/include/secrets.h"}

# Every macro that must not survive into the image.  OTA_PASS is last and is
# the one most easily forgotten; it is a credential like any other.
macros="WIFI_SSID WIFI_PASS USAGED_HOST USAGED_DEVICE_TOKEN OTA_PASS"

# --- helpers -----------------------------------------------------------------

# Prints the string literal defined for macro $1, or nothing when the macro is
# absent or is not a string.  awk rather than sed -E for the same reason
# upload_ota.sh uses awk: it is the portable tool here.  Takes the first quoted
# run so a trailing comment cannot bleed into the value.
macro_value() {
    awk -v name="$1" '
        $1 == "#define" && $2 == name {
            q1 = index($0, "\"")
            if (q1 == 0) exit
            rest = substr($0, q1 + 1)
            q2 = index(rest, "\"")
            if (q2 == 0) exit
            print substr(rest, 1, q2 - 1)
            exit
        }
    ' "$secrets_file"
}

# The placeholders shipped in secrets.h.example.  Comparing a binary against a
# placeholder proves nothing, so those macros are skipped rather than "passed".
is_placeholder() {
    case $1 in
        ...|set-a-unique-ota-password) return 0 ;;
    esac
    return 1
}

# True when $2 appears in the binary $1.  `strings -n` emits only runs at least
# as long as the value, which is both faster and, unlike the default -n 4,
# cannot miss a value shorter than four characters.  The exact whole-line match
# comes first because a credential is its own NUL-terminated literal; the
# substring match is the safety net, since the compiler may tail-merge one
# literal into the end of another and an exact match would then miss a real
# leak.  `--` keeps a value with a leading dash from being read as an option.
binary_contains() {
    contains_bin=$1
    contains_val=$2
    if strings -n "${#contains_val}" "$contains_bin" | grep -qxF -- "$contains_val"; then
        return 0
    fi
    if strings -n "${#contains_val}" "$contains_bin" | grep -qF -- "$contains_val"; then
        return 0
    fi
    return 1
}

# --- preconditions -----------------------------------------------------------

if [ ! -f "$bin" ]; then
    echo "no firmware binary at $bin — run 'make fw-build' first, or pass a path" >&2
    exit 1
fi

if ! command -v strings >/dev/null 2>&1; then
    echo "'strings' is not installed; cannot inspect $bin" >&2
    exit 1
fi

# A build with no secrets.h is the case this whole task is creating, so it is
# not a failure — there is simply nothing to compare against.
if [ ! -f "$secrets_file" ]; then
    echo "no secrets.h at $secrets_file — nothing to compare against; not a failure"
    echo "PASS (0 checked, no configured credentials on this machine)"
    exit 0
fi

# --- the check ---------------------------------------------------------------

echo "== checking $bin against the macros configured in include/secrets.h =="

checked=0
skipped=0
leaked=""

for macro in $macros; do
    value=$(macro_value "$macro")

    if [ -z "$value" ] || is_placeholder "$value"; then
        echo "SKIP $macro — not set, or still the secrets.h.example placeholder"
        skipped=$((skipped + 1))
        continue
    fi

    # C escapes are not decoded here, so a literal containing a backslash may
    # not match the decoded bytes in the image.  Say so instead of reporting a
    # pass that was never really tested.
    case $value in
        *\\*) echo "WARN $macro — value contains a backslash escape; this comparison is unreliable" ;;
    esac

    checked=$((checked + 1))
    if binary_contains "$bin" "$value"; then
        echo "FAIL $macro — its configured value is present in the binary"
        leaked="$leaked $macro"
    else
        echo "PASS $macro"
    fi
done

# --- verdict -----------------------------------------------------------------

if [ -n "$leaked" ]; then
    echo "FAIL: credentials found in $bin —$leaked"
    echo "this binary must not be published or shared; move these values to NVS"
    exit 1
fi

if [ "$checked" -eq 0 ]; then
    echo "PASS (0 checked, $skipped skipped — nothing configured to compare against)"
    exit 0
fi

echo "PASS: none of the $checked configured credential(s) appear in $bin ($skipped skipped)"
exit 0
