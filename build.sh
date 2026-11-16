#!/usr/bin/env bash
# Cross-Compile: dist/brain_<version>_<os>_<arch>.(zip|tar.gz) + dist/SHA256SUMS
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${VERSION:-0.2.0-rc.5}"
TARGETS="${TARGETS:-windows/amd64 windows/arm64 darwin/amd64 darwin/arm64 linux/amd64 linux/arm64}"
rm -rf dist && mkdir -p dist
python3 - "$VERSION" > dist/MANIFEST.json <<'PY'
import json, subprocess, sys
def git(*args):
    try:
        return subprocess.check_output(["git", *args], stderr=subprocess.DEVNULL, text=True).strip()
    except (OSError, subprocess.CalledProcessError):
        return "unknown"
trailers = {}
for line in git("log", "-1", "--format=%B").splitlines():
    key, _, value = line.partition(": ")
    if key in ("Paperclip-Issue", "Paperclip-Root"):
        trailers[key] = value
json.dump({"version":sys.argv[1], "source":"https://github.com/servas-ai/brain-cli",
    "source_revision":git("rev-parse", "HEAD"), "source_dirty":git("status", "--porcelain") != "",
    "paperclip":trailers, "artifact_kind":"local cross-build", "signature":"none",
    "validation":"See PR/issue evidence; cross-compilation is not native runtime acceptance",
    "activation":"No IdP registration, grant or KIRA deployment performed by this build"}, sys.stdout, indent=2)
print()
PY
for t in $TARGETS; do
  os="${t%/*}"; arch="${t#*/}"
  name="brain_${VERSION}_${os}_${arch}"
  stage="dist/.stage/$name"; mkdir -p "$stage"
  bin="brain"; [ "$os" = windows ] && bin="brain.exe"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o "$stage/$bin" .
  cp README.md LICENSE THIRD-PARTY.md CHANGELOG.md dist/MANIFEST.json "$stage/"
  cp -R docs "$stage/docs"
  if [ "$os" = windows ]; then
    if command -v zip >/dev/null 2>&1; then
      (cd "$stage" && zip -qr "../../$name.zip" .)
    else
      python3 - "$stage" "dist/$name.zip" <<'PY'
import pathlib, sys, zipfile
root = pathlib.Path(sys.argv[1])
with zipfile.ZipFile(sys.argv[2], "w", zipfile.ZIP_DEFLATED) as archive:
    for path in sorted(root.rglob("*")):
        if path.is_file():
            archive.write(path, path.relative_to(root))
PY
    fi
  else
    tar -C "$stage" -czf "dist/$name.tar.gz" .
  fi
  echo "built: $name"
done
rm -rf dist/.stage
(cd dist && sha256sum brain_* MANIFEST.json > SHA256SUMS)
cat dist/SHA256SUMS
