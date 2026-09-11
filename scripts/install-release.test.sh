#!/bin/bash
# install-release.test.sh — the installer mints credentials the wire must accept.
#
# WHY THIS EXISTS. v0.3.0 shipped a generator that produced a 64-character OTA
# password against a hard limit of 63 (bleprov.MaxOtaPass, kMaxOtaPass). The
# record was rejected locally, before any radio traffic, so EVERY Bluetooth
# setup failed while the dashboard told the owner to press a button on the
# device. The generator's own comment said "~32 chars"; nobody measured it.
#
# Run: bash scripts/install-release.test.sh   (wired into `make verify`)
set -uo pipefail
cd "$(dirname "$0")/.."
SRC="scripts/install-release.sh"
MAX_OTA=63
MAX_TOKEN=64
fails=0
ok()   { echo "  ok   $1"; }
bad()  { echo "  FAIL $1 — $2"; fails=$((fails + 1)); }

# The limits must still be what this test believes they are.
GO_MAX=$(grep -E '^\s*MaxOtaPass\s*=' internal/bleprov/wire.go | grep -oE '[0-9]+')
FW_MAX=$(grep -E 'kMaxOtaPass\s*=' firmware/src/usage/provision.h | grep -oE '[0-9]+')
echo "== the two sides agree on the limit =="
[ "$GO_MAX" = "$MAX_OTA" ] && ok "go MaxOtaPass = $MAX_OTA" || bad "go MaxOtaPass" "is $GO_MAX, expected $MAX_OTA"
[ "$FW_MAX" = "$MAX_OTA" ] && ok "firmware kMaxOtaPass = $MAX_OTA" || bad "firmware kMaxOtaPass" "is $FW_MAX, expected $MAX_OTA"

echo "== the generators produce values the wire accepts =="
# Extract and run the real generator lines, so this tests the shipped code and
# not a copy that can drift away from it.
# Anchor on the ASSIGNMENT, never on the command text: the file also documents
# the broken v0.3.0 generator inside a comment, and matching that instead would
# have this test grade a line nothing runs.
OTA_GEN=$(grep -m1 -E '^[[:space:]]*OTA_PASS="\$\(od ' "$SRC" | sed -E 's/.*\$\((.*)\)".*/\1/')
TOK_GEN=$(grep -m1 -E '^[[:space:]]*TOKEN="\$\(od ' "$SRC" | sed -E 's/.*\$\((.*)\)".*/\1/')
[ -n "$OTA_GEN" ] && ok "found the live OTA generator" || bad "found the OTA generator" "no OTA_PASS=\$(od ...) assignment in $SRC"
[ -n "$TOK_GEN" ] && ok "found the live token generator" || bad "found the token generator" "no TOKEN=\$(od ...) assignment in $SRC"

for i in 1 2 3; do
    OTA=$(eval "$OTA_GEN")
    if [ "${#OTA}" -le "$MAX_OTA" ]; then ok "ota password fits ($((${#OTA})) <= $MAX_OTA)"
    else bad "ota password fits" "generated ${#OTA} chars, limit $MAX_OTA"; fi
    case "$OTA" in
        "" | *[![:graph:]]*) bad "ota password is printable" "contains whitespace or is empty" ;;
        *) ok "ota password is printable" ;;
    esac
done
TOK=$(eval "$TOK_GEN")
[ "${#TOK}" -le "$MAX_TOKEN" ] && ok "token fits (${#TOK} <= $MAX_TOKEN)" || bad "token fits" "generated ${#TOK} chars"

echo "== an unusable stored password is REPLACED, not reused =="
# The v0.3.0 value, verbatim in shape: 64 characters.
OLD=$(printf 'a%.0s' $(seq 1 64))
# Drive the installer's own predicate rather than re-implementing it, with the
# limit it reads taken from the installer too.
eval "$(grep -m1 '^OTA_PASS_MAX=' "$SRC")"
eval "$(sed -n '/^ota_pass_usable()/,/^}/p' "$SRC")"
[ "${OTA_PASS_MAX:-}" = "$MAX_OTA" ] && ok "installer limit = $MAX_OTA" || bad "installer limit" "is ${OTA_PASS_MAX:-unset}"
if ota_pass_usable "$OLD"; then bad "64 chars rejected" "the installer would reuse it"; else ok "64 chars rejected"; fi
if ota_pass_usable "$(eval "$OTA_GEN")"; then ok "a fresh value is accepted"; else bad "a fresh value is accepted" "predicate refuses its own generator"; fi
if ota_pass_usable ""; then bad "empty rejected" "empty accepted"; else ok "empty rejected"; fi
if ota_pass_usable "has space"; then bad "whitespace rejected" "accepted a value with a space"; else ok "whitespace rejected"; fi

if [ "$fails" -ne 0 ]; then echo; echo "install-release: $fails check(s) failed"; exit 1; fi
echo; echo "install-release: all checks passed"
