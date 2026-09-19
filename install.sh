#!/usr/bin/env sh
# seaglass installer. Downloads the latest release binary for your platform.
#
#   curl -fsSL https://raw.githubusercontent.com/ctrl-research/seaglass/main/install.sh | sh
#
# Env:
#   SEAGLASS_VERSION  install a specific version (e.g. v0.2.0); default latest
#   SEAGLASS_BIN_DIR  install directory; default ~/.local/bin
set -eu

REPO="ctrl-research/seaglass"
BIN_DIR="${SEAGLASS_BIN_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "seaglass: unsupported architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in
  darwin | linux) ;;
  *) echo "seaglass: unsupported OS: $os (use the Homebrew tap or build from source)" >&2; exit 1 ;;
esac

version="${SEAGLASS_VERSION:-}"
if [ -z "$version" ]; then
  version=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep '"tag_name"' | head -n1 | sed -E 's/.*"([^"]+)".*/\1/')
fi
if [ -z "$version" ]; then
  echo "seaglass: could not determine the latest version" >&2
  exit 1
fi

ver_no_v=${version#v}
archive="seaglass_${ver_no_v}_${os}_${arch}.tar.gz"
url="https://github.com/$REPO/releases/download/$version/$archive"

echo "seaglass: downloading $version for $os/$arch"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
if ! curl -fSL "$url" -o "$tmp/$archive"; then
  echo "seaglass: download failed: $url" >&2
  exit 1
fi

# Verify the checksum when available.
if curl -fsSL "https://github.com/$REPO/releases/download/$version/checksums.txt" -o "$tmp/checksums.txt" 2>/dev/null; then
  ( cd "$tmp" && grep " $archive\$" checksums.txt | (sha256sum -c - 2>/dev/null || shasum -a 256 -c - 2>/dev/null) ) \
    || { echo "seaglass: checksum verification failed" >&2; exit 1; }
fi

tar -xzf "$tmp/$archive" -C "$tmp"
mkdir -p "$BIN_DIR"
install -m 0755 "$tmp/seaglass" "$BIN_DIR/seaglass"

echo "seaglass: installed to $BIN_DIR/seaglass"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "seaglass: add $BIN_DIR to your PATH: export PATH=\"$BIN_DIR:\$PATH\"" ;;
esac
"$BIN_DIR/seaglass" --version 2>/dev/null || true
