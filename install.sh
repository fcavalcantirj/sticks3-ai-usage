#!/bin/bash
# install.sh — one-line installer.
#
#   curl -fsSL https://raw.githubusercontent.com/fcavalcantirj/sticks3-ai-usage/main/install.sh | bash
#
# Downloads the latest release, checks it against the published SHA256, and
# runs the installer inside it. Everything it does lives in this file and the
# one it unpacks; read both before piping anything to a shell — including this.
#
# A NICE SIDE EFFECT OF INSTALLING THIS WAY: curl does not set the
# com.apple.quarantine attribute that a browser download gets, so macOS never
# raises the "unidentified developer" dialog. Nothing is being worked around —
# the binary is simply not arriving through the path Gatekeeper polices.
set -euo pipefail

REPO="fcavalcantirj/sticks3-ai-usage"
API="https://api.github.com/repos/${REPO}/releases/latest"

[ "$(uname -s)" = "Darwin" ] || {
    echo "usaged is macOS only — it reads the macOS Keychain and drives CoreBluetooth." >&2
    exit 1
}

echo "== usaged =="

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "-- finding the latest release"
TAG="$(curl -fsSL "$API" | awk -F'"' '/"tag_name"/{print $4; exit}')"
[ -n "$TAG" ] || { echo "FAIL: could not read the latest release tag" >&2; exit 1; }
echo "   ${TAG}"

TARBALL="usaged-${TAG}-darwin-universal.tar.gz"
BASE="https://github.com/${REPO}/releases/download/${TAG}"

echo "-- downloading ${TARBALL}"
curl -fsSL -o "${TMP}/${TARBALL}" "${BASE}/${TARBALL}"
curl -fsSL -o "${TMP}/SHA256SUMS"  "${BASE}/SHA256SUMS"

# Verify before unpacking, not after. The published SHA256SUMS lists the file
# as "./name", so compare the digests rather than trying to match the format.
echo "-- checking the download"
GOT="$(shasum -a 256 "${TMP}/${TARBALL}" | awk '{print $1}')"
WANT="$(awk -v f="$TARBALL" '$2 ~ f {print $1; exit}' "${TMP}/SHA256SUMS")"
[ -n "$WANT" ] || { echo "FAIL: ${TARBALL} is not listed in SHA256SUMS" >&2; exit 1; }
[ "$GOT" = "$WANT" ] || {
    echo "FAIL: checksum mismatch — do not run this download." >&2
    echo "      got  ${GOT}" >&2
    echo "      want ${WANT}" >&2
    exit 1
}
echo "   ok"

tar -xzf "${TMP}/${TARBALL}" -C "$TMP"
exec bash "${TMP}/usaged-${TAG}-darwin-universal/install.sh"
