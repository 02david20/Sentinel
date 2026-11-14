#!/bin/sh
# Installs the latest (or a chosen) Sentinel release without a Go toolchain.
#   curl -fsSL https://raw.githubusercontent.com/02david20/Sentinel/main/scripts/install.sh | sh
# Options (environment variables):
#   SENTINEL_VERSION   a tag such as v0.2.0 (default: the latest release)
#   SENTINEL_PREFIX    install into $SENTINEL_PREFIX/bin (default: $HOME/.local)
#   SENTINEL_REPO      owner/name on GitHub (default: 02david20/Sentinel)
#   SENTINEL_BASE_URL  a folder holding the archives and checksums.txt (testing and mirrors)
# Pass --dry-run to print what would happen and change nothing.
set -eu

REPO="${SENTINEL_REPO:-02david20/Sentinel}"
PREFIX="${SENTINEL_PREFIX:-$HOME/.local}"
VERSION="${SENTINEL_VERSION:-latest}"
DRY=0
[ "${1:-}" = "--dry-run" ] && DRY=1

say() { printf '%s\n' "$*"; }
die() { printf 'sentinel install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but was not found"; }

need curl; need tar; need uname
case "$(uname -s)" in
  Linux)  OS=linux ;;
  Darwin) OS=darwin ;;
  *) die "unsupported system $(uname -s). Sentinel runs on Linux and macOS (see docs/INSTALL.md)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) die "unsupported CPU $(uname -m)" ;;
esac

if [ -n "${SENTINEL_BASE_URL:-}" ]; then
  [ "$VERSION" != latest ] || die "set SENTINEL_VERSION when using SENTINEL_BASE_URL"
  BASE="$SENTINEL_BASE_URL"
else
  if [ "$VERSION" = latest ]; then
    # Follow the redirect of /releases/latest: no API call, no rate limit, no token.
    VERSION="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" | sed 's#.*/tag/##')" || true
    case "$VERSION" in v[0-9]*) ;; *) die "could not find a release of $REPO (has one been published?)" ;; esac
  fi
  BASE="https://github.com/$REPO/releases/download/$VERSION"
fi
NUM="${VERSION#v}"
ARCHIVE="sentinel_${NUM}_${OS}_${ARCH}.tar.gz"

say "Sentinel $VERSION for $OS/$ARCH"
say "  from:  $BASE/$ARCHIVE"
say "  to:    $PREFIX/bin/sentinel"
[ "$DRY" = 1 ] && { say "(dry run: nothing downloaded or installed)"; exit 0; }

TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT INT TERM
curl -fsSL "$BASE/$ARCHIVE" -o "$TMP/$ARCHIVE" || die "download failed: $BASE/$ARCHIVE"
curl -fsSL "$BASE/checksums.txt" -o "$TMP/checksums.txt" || die "download failed: $BASE/checksums.txt"

# Verify the archive against the published checksum before running anything from it.
WANT="$(grep "  $ARCHIVE\$" "$TMP/checksums.txt" | cut -d' ' -f1)"
[ -n "$WANT" ] || die "$ARCHIVE is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then GOT="$(sha256sum "$TMP/$ARCHIVE" | cut -d' ' -f1)"
elif command -v shasum >/dev/null 2>&1; then GOT="$(shasum -a 256 "$TMP/$ARCHIVE" | cut -d' ' -f1)"
else die "sha256sum or shasum is required to verify the download"; fi
[ "$WANT" = "$GOT" ] || die "checksum mismatch for $ARCHIVE (expected $WANT, got $GOT); not installing"

tar -xzf "$TMP/$ARCHIVE" -C "$TMP" ./sentinel || die "the archive does not contain ./sentinel"
mkdir -p "$PREFIX/bin"
install -m 0755 "$TMP/sentinel" "$PREFIX/bin/sentinel"
[ "$OS" = darwin ] && xattr -d com.apple.quarantine "$PREFIX/bin/sentinel" 2>/dev/null || true

say "Installed: $("$PREFIX/bin/sentinel" version)"
case ":$PATH:" in
  *":$PREFIX/bin:"*) ;;
  *) say "Note: $PREFIX/bin is not on your PATH. Add it:  export PATH=\"$PREFIX/bin:\$PATH\"" ;;
esac
say "Next:  sentinel doctor   then   sentinel run --dry-run <workflow.yaml>   (docs/INSTALL.md)"
