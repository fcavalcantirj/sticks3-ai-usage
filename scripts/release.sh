#!/bin/bash
# release.sh — cut a release with nothing left to remember.
#
# WHY THIS EXISTS. v0.1.9 shipped with the stick reporting fw=1.0.0 because the
# firmware version was a literal nobody bumped, and the M5Burner upload silently
# lagged a day behind the daemon — which is worse than cosmetic: the daemon
# always serves six providers, and a firmware built before the providers[6]
# clamp overflows its array on a six-provider payload. Both failures were
# invisible until someone went looking. This script makes them impossible to
# miss: it refuses to tag when versions disagree, and it does not exit quietly
# without telling you the firmware still has to reach M5Burner by hand.
#
#   sh scripts/release.sh v0.2.0            full release
#   sh scripts/release.sh v0.2.0 --dry-run  every check, no tag, no publish
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-}"
DRY_RUN=0
[ "${2:-}" = "--dry-run" ] && DRY_RUN=1
case "$VERSION" in
    v[0-9]*.[0-9]*.[0-9]*) ;;
    *) echo "usage: sh scripts/release.sh vX.Y.Z [--dry-run]" >&2; exit 2 ;;
esac
BARE="${VERSION#v}"
FW_BUILD_DIR="firmware/.pio/build/m5stack-sticks3"
FW_BIN="$FW_BUILD_DIR/firmware.bin"
ASSET="dist/ai-usage-firmware-${VERSION}-m5sticks3.bin"
TARBALL="dist/ai-usage-${VERSION}-darwin-universal.tar.gz"

step() { printf '\n== %s ==\n' "$1"; }
die()  { printf '\nREFUSING: %s\n' "$1" >&2; exit 1; }

step "1/8  working tree"
if [ -n "$(git status --porcelain)" ]; then
    echo "uncommitted changes:" >&2
    git status --porcelain >&2
    die "working tree is dirty — commit or stash first"
fi
[ -z "$(git log --oneline "origin/$(git rev-parse --abbrev-ref HEAD)..HEAD" 2>/dev/null)" ] \
    || echo "   note: local commits are not pushed yet; this script pushes them"
git rev-parse "$VERSION" >/dev/null 2>&1 && die "$VERSION already exists — pick a new version"

step "2/8  host suite"
make verify
go test -race ./... >/dev/null && echo "   go test -race: PASS"

step "3/8  firmware suite"
make fw-test
make fw-build

step "4/8  firmware carries no credentials"
make fw-publish-check | tee /tmp/rel-pub.log
grep -q '^PUBLISHABLE' /tmp/rel-pub.log || die "fw-publish-check did not say PUBLISHABLE"

step "5/8  VERSION ALIGNMENT — the check that would have caught v0.1.9"
# The tag has to exist before build_id.py can derive the version from it, so
# create it locally first; step 8 deletes it again on a dry run.
# The tag has to exist before build_id.py can read it, but a run that dies
# between here and the end must not leave it behind — an interrupted dry run
# did exactly that, and the next attempt refused with "already exists".
TAG_IS_TEMP=1
cleanup_tag() { [ "${TAG_IS_TEMP:-0}" = 1 ] && git tag -d "$VERSION" >/dev/null 2>&1 || true; }
trap cleanup_tag EXIT INT TERM
git tag -a "$VERSION" -m "$VERSION" >/dev/null
# BUILD THE IMAGE WE ACTUALLY SHIP, WITH NO secrets.h PRESENT.
#
# THIS IS THE BUG THAT LEAKED A WI-FI PASSWORD. Step 4 above proves a
# SECRETS-FREE build is clean and then puts secrets.h back. This line used to
# rebuild with it present — so the script validated one binary and published a
# different one, and the v0.3.0 and v0.3.1 firmware assets went to GitHub (and
# to M5Burner) carrying the builder's WIFI_SSID, WIFI_PASS, USAGED_HOST,
# USAGED_DEVICE_TOKEN and OTA_PASS as plaintext strings. Verified after the
# fact with `strings`: one occurrence of each.
#
# The asset must be built the same way it was checked. secrets.h is moved aside
# for the build and restored on EXIT whatever happens.
SECRETS="firmware/include/secrets.h"
SECRETS_STASH="$SECRETS.release"
restore_secrets() { [ -f "$SECRETS_STASH" ] && mv -f "$SECRETS_STASH" "$SECRETS" || true; }
cleanup_all() { restore_secrets; cleanup_tag; }
trap cleanup_all EXIT INT TERM HUP
# `[ -f x ] && mv` as a bare statement returns 1 when the file is absent, and
# `set -e` would kill the release there. Guard it.
if [ -f "$SECRETS" ]; then mv "$SECRETS" "$SECRETS_STASH"; fi
( cd firmware && platformio run -e m5stack-sticks3 ) >/tmp/rel-fw.log 2>&1 \
    || die "firmware rebuild failed — see /tmp/rel-fw.log"
FW_REPORTED=$(grep -o 'USAGED_FW_VERSION=[^ ]*' /tmp/rel-fw.log | tail -1 | cut -d= -f2)
echo "   tag              $VERSION"
echo "   firmware reports $FW_REPORTED"
if [ "$FW_REPORTED" != "$BARE" ]; then
    die "firmware reports '$FW_REPORTED' but the tag is '$VERSION' — they must match"
fi

# PUBLISH A MERGED, BOOTABLE IMAGE — NOT firmware.bin.
#
# v0.2.0 shipped .pio/build/<env>/firmware.bin, which is the APPLICATION IMAGE
# ONLY and belongs at offset 0x10000. M5Burner writes what you give it starting
# at 0x0, so the ROM found no bootloader and the device died with
# "Invalid image block, can't boot. ets_main.c 329". firmware.bin is the right
# artifact for ArduinoOTA (which writes the app partition) and the wrong one for
# anything that flashes at 0x0.
mkdir -p dist
BOOT_APP0="$HOME/.platformio/packages/framework-arduinoespressif32/tools/partitions/boot_app0.bin"
PIO_PY="$(ls "$HOME"/.platformio/penv/bin/python 2>/dev/null || command -v python3)"
[ -f "$BOOT_APP0" ] || die "boot_app0.bin not found — cannot build a bootable image"
"$PIO_PY" -m esptool --chip esp32s3 merge_bin -o "$ASSET" \
    --flash_mode dio --flash_freq 80m --flash_size 8MB \
    0x0     "$FW_BUILD_DIR/bootloader.bin" \
    0x8000  "$FW_BUILD_DIR/partitions.bin" \
    0xe000  "$BOOT_APP0" \
    0x10000 "$FW_BIN" >/dev/null || die "esptool merge_bin failed"

# Both an app-only image and a merged one start with 0xE9, so the magic byte
# does NOT tell them apart. The partition table at 0x8000 does: 0xAA50.
PT_MAGIC=$(xxd -s 0x8000 -l 2 -p "$ASSET")
[ "$PT_MAGIC" = "aa50" ] || die "the asset has no partition table at 0x8000 (got $PT_MAGIC) — it is not bootable at 0x0"
# `strings … | grep -q` races the pipe buffer exactly like scripts/smoke.sh:165:
# grep matches a few KB in and exits, strings dies with EPIPE, and `set -o
# pipefail` turns that 141 into "the asset has no version string". It refused
# EVERY release, not just a mismatched one — v0.2.0 and v0.3.0 both died here
# with the string present. Drain the whole stream instead of exiting early.
strings "$ASSET" | grep -xF "$BARE" >/dev/null || die "the firmware asset does not contain the version string $BARE"

# CHECK THE ARTIFACT THAT IS ABOUT TO BE UPLOADED, not one built like it.
# Step 4 checks firmware.bin; this checks the merged image byte-for-byte, so a
# future reordering cannot reintroduce the leak silently.
restore_secrets
sh firmware/scripts/check_no_secrets.sh "$ASSET" >/tmp/rel-asset-check.log 2>&1 \
    || { cat /tmp/rel-asset-check.log >&2; die "THE PUBLISHED ASSET CONTAINS A CREDENTIAL — refusing to release"; }
echo "   asset re-checked against secrets.h: none of the five values are present"
echo "   firmware asset: bootable (partition table at 0x8000), reports $BARE, $(wc -c < "$ASSET" | tr -d ' ') bytes"

step "6/8  package"
make dist VERSION="$VERSION"
# dist.sh clears dist/, so rebuild the merged asset it wiped
"$PIO_PY" -m esptool --chip esp32s3 merge_bin -o "$ASSET" \
    --flash_mode dio --flash_freq 80m --flash_size 8MB \
    0x0 "$FW_BUILD_DIR/bootloader.bin" 0x8000 "$FW_BUILD_DIR/partitions.bin" \
    0xe000 "$BOOT_APP0" 0x10000 "$FW_BIN" >/dev/null || die "esptool merge_bin failed"
( cd dist && shasum -a 256 "$(basename "$TARBALL")" "$(basename "$ASSET")" > SHA256SUMS )
# install.sh greps SHA256SUMS by filename rather than running `shasum -c`,
# so extra lines are fine — but the tarball line must be present and correct.
WANT=$(awk -v f="$(basename "$TARBALL")" '$2 ~ f {print $1; exit}' dist/SHA256SUMS)
GOT=$(shasum -a 256 "$TARBALL" | awk '{print $1}')
[ -n "$WANT" ] && [ "$WANT" = "$GOT" ] || die "SHA256SUMS does not match the tarball install.sh will fetch"
echo "   tarball checksum verified the way install.sh verifies it"

if [ "$DRY_RUN" = 1 ]; then
    printf '\nDRY RUN: every check passed. Nothing was tagged or published.\n'
    exit 0
fi

TAG_IS_TEMP=0   # past this point the tag is real and must survive
step "7/8  publish"
git push origin HEAD
git push origin "$VERSION"
gh release create "$VERSION" "$TARBALL" "$ASSET" dist/SHA256SUMS \
    --title "$VERSION" --generate-notes
LATEST=$(curl -fsSL https://api.github.com/repos/fcavalcantirj/sticks3-ai-usage/releases/latest \
         | awk -F'"' '/"tag_name"/{print $4; exit}')
[ "$LATEST" = "$VERSION" ] || die "releases/latest still says $LATEST — the install one-liner would fetch the wrong version"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -L \
       "https://github.com/fcavalcantirj/sticks3-ai-usage/releases/download/$VERSION/ai-usage-$VERSION-darwin-universal.tar.gz")
[ "$CODE" = "200" ] || die "the URL install.sh builds returns $CODE — the headline install path is broken"
echo "   install one-liner resolves to $VERSION and returns 200"

step "8/8  THE STEP NO SCRIPT CAN DO FOR YOU"
cat <<EOF

  The daemon is released. THE FIRMWARE IS NOT PUBLISHED YET.

  Upload this file to M5Burner now:

      $ASSET

  Until you do, anyone flashing from M5Burner gets the PREVIOUS firmware while
  install.sh gives them the NEW daemon. That mismatch is not cosmetic: the
  daemon always serves six providers, and firmware older than the providers[6]
  clamp writes past the end of its array on a six-provider payload.

  After uploading, confirm the M5Burner entry reads $BARE — the version shown
  there is whatever the .bin reports, so a stale upload looks correct.

EOF
