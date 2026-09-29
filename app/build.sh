#!/usr/bin/env sh
# Builds netmon for Windows and Linux into ../dist (no C compiler needed).
# Usage: ./build.sh [version]     e.g. ./build.sh 1.0.0
set -e
cd "$(dirname "$0")"
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT=../dist
rm -rf "$OUT" && mkdir -p "$OUT"
for target in windows/amd64 windows/arm64 linux/amd64 linux/arm64 linux/arm; do
  os=${target%/*}; arch=${target#*/}
  name="netmon-$os-$arch"; gui=""
  # Windows: GUI program (tray icon, no console window)
  [ "$os" = windows ] && name="$name.exe" && gui="-H windowsgui"
  echo "building $name"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOARM=7 go build -trimpath \
    -ldflags "-s -w $gui -X main.version=$VERSION" -o "$OUT/$name" .
done
( cd "$OUT" && sha256sum netmon-* > SHA256SUMS.txt )
ls -l "$OUT"
