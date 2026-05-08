#!/usr/bin/env sh
# install.sh - one-command zenflow installer for macOS and Linux.
#
# Usage:
#   curl -fsSL https://zenflow.sh/install.sh | sh
#
# Pin a specific version:
#   curl -fsSL https://zenflow.sh/install.sh | ZENFLOW_VERSION=v0.1.0-pre sh
#
# Install to a different directory:
#   curl -fsSL https://zenflow.sh/install.sh | ZENFLOW_INSTALL_DIR=/usr/local/bin sh
#
# Detects OS (Darwin / Linux) and arch (x86_64 / arm64), fetches the
# matching tarball + checksums.txt from the latest GitHub Release,
# verifies SHA-256, extracts to ZENFLOW_INSTALL_DIR (default
# $HOME/.local/bin), and prints a PATH hint if the dir is not yet
# on PATH.

set -eu

REPO="zendev-sh/zenflow"
INSTALL_DIR="${ZENFLOW_INSTALL_DIR:-$HOME/.local/bin}"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT INT TERM

# ---------- helpers ----------
err() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[36m==>\033[0m %s\n' "$*"; }
need() { command -v "$1" >/dev/null 2>&1 || err "missing required tool: $1"; }

need uname
need curl
need tar
if ! command -v shasum >/dev/null 2>&1 && ! command -v sha256sum >/dev/null 2>&1; then
  err "missing required tool: shasum or sha256sum"
fi

# ---------- detect OS + arch ----------
OS="$(uname -s)"
case "$OS" in
  Darwin) GOOS="darwin" ;;
  Linux) GOOS="linux" ;;
  *) err "unsupported OS: $OS (zenflow ships for darwin and linux via this installer; use scoop or manual download on Windows)" ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64 | amd64) GOARCH="x86_64" ;;
  arm64 | aarch64) GOARCH="arm64" ;;
  *) err "unsupported arch: $ARCH" ;;
esac

# ---------- resolve version ----------
if [ -n "${ZENFLOW_VERSION:-}" ]; then
  TAG="$ZENFLOW_VERSION"
else
  info "resolving latest release"
  TAG="$(curl -fsSL --proto '=https' --tlsv1.2 --max-time 60 "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' \
    | head -n1)"
  if [ -z "$TAG" ]; then
    # No stable release yet (prerelease-only); fall back to most-recent release.
    TAG="$(curl -fsSL --proto '=https' --tlsv1.2 --max-time 60 "https://api.github.com/repos/$REPO/releases?per_page=1" \
      | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' \
      | head -n1)"
  fi
  [ -n "$TAG" ] || err "could not resolve any release tag"
fi

# goreleaser archive name_template uses {{ .Version }} which strips the leading
# `v` from the tag, so URLs must use the un-prefixed version even when the tag
# itself is `vX.Y.Z`.
VER="${TAG#v}"

ARCHIVE="zenflow_${VER}_${GOOS}_${GOARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$TAG/$ARCHIVE"
SUMS_URL="https://github.com/$REPO/releases/download/$TAG/checksums.txt"

# ---------- download ----------
info "downloading $ARCHIVE"
curl -fsSL --proto '=https' --tlsv1.2 --max-time 60 "$URL" -o "$TMPDIR/$ARCHIVE" || err "download failed: $URL"

info "downloading checksums.txt"
curl -fsSL --proto '=https' --tlsv1.2 --max-time 60 "$SUMS_URL" -o "$TMPDIR/checksums.txt" || err "download failed: $SUMS_URL"

# ---------- verify ----------
info "verifying SHA-256"
EXPECTED="$(awk -v a="$ARCHIVE" '$2 == a {print $1}' "$TMPDIR/checksums.txt")"
[ -n "$EXPECTED" ] || err "checksum line for $ARCHIVE not found in checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL="$(sha256sum "$TMPDIR/$ARCHIVE" | awk '{print $1}')"
else
  ACTUAL="$(shasum -a 256 "$TMPDIR/$ARCHIVE" | awk '{print $1}')"
fi

if [ "$EXPECTED" != "$ACTUAL" ]; then
  err "SHA-256 mismatch (expected $EXPECTED, got $ACTUAL); refusing to install tampered archive"
fi

# ---------- install ----------
info "extracting"
# --no-same-owner: defence-in-depth against tarballs with crafted ownership
tar -xzf "$TMPDIR/$ARCHIVE" -C "$TMPDIR" --no-same-owner

mkdir -p "$INSTALL_DIR" || err "could not create install dir: $INSTALL_DIR"
mv "$TMPDIR/zenflow" "$INSTALL_DIR/zenflow" || err "could not move binary to $INSTALL_DIR"
chmod +x "$INSTALL_DIR/zenflow"

info "installed zenflow $TAG to $INSTALL_DIR/zenflow"

# ---------- PATH hint ----------
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    printf '\n'
    printf '\033[33mnote:\033[0m %s is not on your PATH.\n' "$INSTALL_DIR"
    printf '  add this line to your shell rc:\n'
    # shellcheck disable=SC2016
    printf '    export PATH="%s:$PATH"\n' "$INSTALL_DIR"
    printf '\n'
    ;;
esac

info "verify with: zenflow --version"
