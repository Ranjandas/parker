#!/bin/sh
# Install Parker: https://ranjandas.github.io/parker/
#
#   curl -fsSL https://ranjandas.github.io/parker/install.sh | sh
#
# Downloads the latest release (or PARKER_VERSION, e.g. 0.9.0) for this OS
# and CPU, checks it against the release's SHA256SUMS, and installs the
# binary into PARKER_INSTALL_DIR (default /usr/local/bin, using sudo when
# that directory isn't writable). On macOS this installs the client, which
# drives a Parker daemon on a Linux host.
set -eu

repo="Ranjandas/parker"
dir="${PARKER_INSTALL_DIR:-/usr/local/bin}"

say() { printf 'parker-install: %s\n' "$*" >&2; }
die() { say "$*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "needs $1"; }

need curl
need tar
need uname

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS $(uname -s): Parker runs on Linux (and macOS as a client)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported CPU $(uname -m): releases are built for amd64 and arm64" ;;
esac

version="${PARKER_VERSION:-}"
version="${version#v}"
if [ -z "$version" ]; then
  # The latest release's URL redirects to .../tag/vX.Y.Z; no API token needed.
  url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest") ||
    die "could not reach GitHub to find the latest release"
  version="${url##*/v}"
  case "$version" in
    [0-9]*.[0-9]*.[0-9]*) ;;
    *) die "could not work out the latest version (got $url); set PARKER_VERSION" ;;
  esac
fi

if command -v sha256sum >/dev/null 2>&1; then
  sha() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  die "needs sha256sum or shasum to check the download"
fi

file="parker_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/v$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading Parker $version for $os/$arch"
curl -fsSL -o "$tmp/$file" "$base/$file" || die "download failed: $base/$file"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" || die "download failed: $base/SHA256SUMS"

want=$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
[ -n "$want" ] || die "$file is not listed in SHA256SUMS"
got=$(sha "$tmp/$file")
[ "$want" = "$got" ] || die "checksum mismatch for $file (want $want, got $got)"

tar -xzf "$tmp/$file" -C "$tmp" parker

if [ -d "$dir" ] && [ -w "$dir" ]; then
  sudo=""
elif [ "$(id -u)" -eq 0 ]; then
  sudo=""
else
  need sudo
  sudo="sudo"
  say "installing into $dir with sudo"
fi
$sudo mkdir -p "$dir"
$sudo install -m 0755 "$tmp/parker" "$dir/parker"

say "installed $dir/parker"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) say "note: $dir is not on your PATH" ;;
esac
"$dir/parker" version 2>/dev/null || true
say "next: parker init lab.yaml && parker up lab.yaml"
say "guide: https://ranjandas.github.io/parker/start.html"
