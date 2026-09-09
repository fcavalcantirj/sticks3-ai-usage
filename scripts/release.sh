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
FW_BIN="firmware/.pio/build/m5stack-sticks3/firmware.bin"
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
( cd firmware && platformio run -e m5stack-sticks3 ) >/tmp/rel-fw.log 2>&1 \
    || die "firmware rebuild failed — see /tmp/rel-fw.log"
FW_REPORTED=$(grep -o 'USAGED_FW_VERSION=[^ ]*' /tmp/rel-fw.log | tail -1 | cut -d= -f2)
echo "   tag              $VERSION"
echo "   firmware reports $FW_REPORTED"
if [ "$FW_REPORTED" != "$BARE" ]; then
    die "firmware reports '$FW_REPORTED' but the tag is '$VERSION' — they must match"
fi

step "6/8  package"
make dist VERSION="$VERSION"
cp "$FW_BIN" "$ASSET"
# Verify the artifact that actually ships, not an intermediate build. The
# earlier draft of this script checked $FW_BIN mid-pipeline and reported a
# mismatch that did not exist.
strings "$ASSET" | grep -qxF "$BARE" || die "the firmware asset does not contain the version string $BARE"
echo "   firmware asset reports $BARE"
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
