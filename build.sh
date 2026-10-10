#!/usr/bin/env bash
# Cross-Compile: dist/brain_<version>_<os>_<arch>.(zip|tar.gz) + dist/SHA256SUMS
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${VERSION:-0.1.2}"
TARGETS="windows/amd64 windows/arm64 darwin/amd64 darwin/arm64 linux/amd64 linux/arm64"
rm -rf dist && mkdir -p dist
for t in $TARGETS; do
  os="${t%/*}"; arch="${t#*/}"
  name="brain_${VERSION}_${os}_${arch}"
  stage="dist/.stage/$name"; mkdir -p "$stage"
  bin="brain"; [ "$os" = windows ] && bin="brain.exe"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o "$stage/$bin" .
  cp README.md LICENSE "$stage/"
  if [ "$os" = windows ]; then
    (cd "$stage" && zip -qr "../../$name.zip" .)
  else
    tar -C "$stage" -czf "dist/$name.tar.gz" .
  fi
  echo "gebaut: $name"
done
rm -rf dist/.stage
(cd dist && sha256sum brain_* > SHA256SUMS)
cat dist/SHA256SUMS
