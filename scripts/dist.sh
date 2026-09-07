#!/bin/bash
# scripts/dist.sh — build the distributable macOS release.
#
# Produces, in dist/:
#   usaged-<version>-darwin-universal.tar.gz   binary + installer + examples
#   SHA256SUMS                                 so a download can be checked
#
# A UNIVERSAL binary because a StickS3 owner may be on an Intel Mac and there
# is no way to ask before they download — one file that runs everywhere beats
# two files and a wrong guess.
#
# IT DOES NOT SIGN FOR DISTRIBUTION, and that is a fact about the certificates
# available, not an oversight. Notarizing needs a "Developer ID Application"
# certificate, which requires the paid Apple Developer Program. So the
# installer clears the quarantine attribute itself and the README says plainly
# why — better than letting a stranger meet Gatekeeper with no explanation.
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
NAME="usaged-${VERSION}-darwin-universal"
OUT="${ROOT}/dist"
STAGE="${OUT}/${NAME}"

echo "== usaged ${VERSION} (${COMMIT}) =="

rm -rf "$OUT"
mkdir -p "$STAGE"

LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}"
# -trimpath so the binary carries no absolute paths from the build machine.
# Cosmetic, but a public binary should not name someone's home directory in
# every panic trace.
BUILDFLAGS="-trimpath"

# CGO is REQUIRED on darwin: the BLE central binds CoreBluetooth through cgo
# (tinygo-org/cbgo). A CGO_ENABLED=0 build compiles and then cannot see a
# single device, which is the worst kind of broken — silent.
echo "-- building arm64"
GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build $BUILDFLAGS -ldflags "$LDFLAGS" -o "${OUT}/usaged-arm64" ./cmd/usaged
echo "-- building amd64"
GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go build $BUILDFLAGS -ldflags "$LDFLAGS" -o "${OUT}/usaged-amd64" ./cmd/usaged

if command -v lipo >/dev/null 2>&1; then
    echo "-- lipo -> universal"
    lipo -create -output "${STAGE}/usaged" "${OUT}/usaged-arm64" "${OUT}/usaged-amd64"
else
    echo "!! lipo not found — shipping the host architecture ONLY"
    cp "${OUT}/usaged-$(go env GOARCH)" "${STAGE}/usaged"
fi
chmod +x "${STAGE}/usaged"
rm -f "${OUT}/usaged-arm64" "${OUT}/usaged-amd64"

# Ad-hoc signing buys NO Gatekeeper trust. It exists so the binary has a stable
# code identity, which is what macOS keys per-app permission grants to —
# including the Bluetooth grant this daemon needs. Unsigned, the identity moves
# on every rebuild and the permission prompt can return after every update.
if command -v codesign >/dev/null 2>&1; then
    echo "-- ad-hoc signing (identity only, NOT notarized)"
    codesign --force --sign - "${STAGE}/usaged" 2>/dev/null \
        || echo "!! ad-hoc signing failed; continuing unsigned"
fi

cp "${ROOT}/scripts/install-release.sh" "${STAGE}/install.sh"
chmod +x "${STAGE}/install.sh"
cp "${ROOT}/config.example.yaml" "${STAGE}/config.example.yaml"

( cd "$OUT" && tar -czf "${NAME}.tar.gz" "$NAME" )
rm -rf "$STAGE"
( cd "$OUT" && shasum -a 256 ./*.tar.gz > SHA256SUMS )

echo
echo "== built =="
ls -lh "$OUT"
cat "${OUT}/SHA256SUMS"
