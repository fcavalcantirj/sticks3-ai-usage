#!/bin/sh
# publish_check.sh — prove a PUBLISHABLE firmware image carries no credentials.
#
# This is the gate task 76 exists to create, and the one thing in the
# provisioning group that must never regress.
#
# Why it is not just `check_no_secrets.sh`: a DEVELOPER build legitimately
# contains the five values, because secrets.h is compiled in as the first-boot
# NVS seed.  Running the check against that build always fails, and a check that
# always fails gets ignored — the same uselessness as one that always passes.
# The meaningful question is: does the image built WITHOUT secrets.h carry them?
#
# So this script does the whole thing atomically:
#   1. move firmware/include/secrets.h aside   (restored on ANY exit)
#   2. build — which also proves the tree still COMPILES with no secrets.h
#   3. grep the resulting binary for all five values, read from the moved header
#   4. restore secrets.h
#
# The output never contains a credential value, only macro names and a verdict.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
firmware_dir=$(dirname "$script_dir")

pio_bin=${PLATFORMIO_CLI_BIN:-platformio}
pio_env=${PIO_ENV:-m5stack-sticks3}
secrets="$firmware_dir/include/secrets.h"
stashed="$firmware_dir/include/secrets.h.publishcheck"
bin="$firmware_dir/.pio/build/$pio_env/firmware.bin"

# Restore on ANY exit path.  Losing secrets.h is unrecoverable — it is
# git-ignored and holds the only copy of this board's OTA password.
restore() {
    if [ -f "$stashed" ] && [ ! -f "$secrets" ]; then
        mv "$stashed" "$secrets"
        echo "secrets.h restored"
    fi
}
trap restore EXIT INT TERM HUP

if [ ! -f "$secrets" ]; then
    echo "no secrets.h at $secrets — build it without one and run check_no_secrets.sh directly" >&2
    exit 1
fi

echo "== moving secrets.h aside and building a publishable image =="
mv "$secrets" "$stashed"

# The build itself is half the assertion: the tree MUST compile with no
# secrets.h, or the production build is impossible regardless of what leaks.
( cd "$firmware_dir" && "$pio_bin" run -e "$pio_env" ) || {
    echo "FAIL: the firmware does not build without secrets.h" >&2
    exit 1
}

echo
echo "== checking the secrets-free image against the stashed header =="
sh "$script_dir/check_no_secrets.sh" "$bin" "$stashed"
rc=$?

echo
if [ "$rc" -eq 0 ]; then
    echo "PUBLISHABLE: built with no secrets.h and none of the five values are present."
fi
exit "$rc"
