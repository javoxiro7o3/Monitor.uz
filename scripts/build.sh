#!/bin/sh
# Builds static binaries and the offline installation package.
# Works without internet: all Go dependencies are in vendor/.
#   ./scripts/build.sh            -> dist/monitor-uz-<ver>-linux-amd64.tar.gz
#   GOARCH=arm64 ./scripts/build.sh
set -eu
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(cat VERSION)}"
GOARCH="${GOARCH:-amd64}"
export CGO_ENABLED=0 GOOS=linux GOARCH GOFLAGS=-mod=vendor GOTOOLCHAIN=local

PKG="monitor-uz-$VERSION-linux-$GOARCH"
OUT="dist/$PKG"
rm -rf "$OUT"
mkdir -p "$OUT/bin" "$OUT/dist" "$OUT/systemd" "$OUT/docs"

LDFLAGS="-s -w -X main.version=$VERSION"
echo "==> go vet / test"
go vet ./...
go test ./... >/dev/null
echo "==> build $GOARCH ($VERSION)"
go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/bin/monitor-hub" ./cmd/hub
go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/bin/monitor-agent" ./cmd/agent

# Agent binaries the hub serves to the install script (/dl/...).
cp "$OUT/bin/monitor-agent" "$OUT/dist/monitor-agent-linux-$GOARCH"
(cd "$OUT/dist" && sha256sum monitor-agent-linux-* > SHA256SUMS && echo "$VERSION" > VERSION)

cp deploy/systemd/*.service "$OUT/systemd/"
cp scripts/install-hub.sh "$OUT/"
cp README.md "$OUT/docs/"
cp docs/*.md "$OUT/docs/"
echo "$VERSION" > "$OUT/VERSION"

tar -C dist -czf "dist/$PKG.tar.gz" "$PKG"
(cd dist && sha256sum "$PKG.tar.gz" > "$PKG.tar.gz.sha256")
ls -l "$OUT/bin" "dist/$PKG.tar.gz"
