#!/bin/sh
# Builds the release archives without GoReleaser, with the same names and layout the pipeline uses:
#   dist/sentinel_<version>_<os>_<arch>.tar.gz   and   dist/checksums.txt
# Usage: scripts/build-dist.sh [version]      (default: from git, e.g. 0.2.0 or 0.2.0-3-gabc123)
set -eu
cd "$(dirname "$0")/.."
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)}"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
TARGETS="${TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64}"
OUT=dist
rm -rf "$OUT"; mkdir -p "$OUT"
for t in $TARGETS; do
  os="${t%/*}"; arch="${t#*/}"
  name="sentinel_${VERSION}_${os}_${arch}"
  stage="$OUT/stage/$name"
  mkdir -p "$stage"
  echo "building $name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE" \
    -o "$stage/sentinel" ./cmd/sentinel
  cp README.md "$stage/"
  [ -d docs ] && cp -R docs "$stage/docs"
  [ -d examples ] && cp -R examples "$stage/examples"
  [ -d workflows ] && cp -R workflows "$stage/workflows"
  ls LICENSE* >/dev/null 2>&1 && cp LICENSE* "$stage/" || true
  # the binary sits at the top level of the archive: tar xzf, then ./sentinel
  (cd "$stage" && tar -czf "../../$name.tar.gz" .)
done
rm -rf "$OUT/stage"
cd "$OUT"
if command -v sha256sum >/dev/null 2>&1; then sha256sum ./*.tar.gz | sed 's#\./##' > checksums.txt
else shasum -a 256 ./*.tar.gz | sed 's#\./##' > checksums.txt; fi
echo; cat checksums.txt
