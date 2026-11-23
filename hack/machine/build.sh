#!/bin/sh
# Build parker for `parker machine` on a Mac (spike):
#   bin/parker        macOS client with VM support (CGO), ad-hoc signed with
#                     the virtualization entitlement Apple requires
#   bin/parker-linux  a dev daemon for the VM, for this Mac's CPU. The VM
#                     runs a release by default; to run this build instead:
#                     PARKER_MACHINE_BINARY=bin/parker-linux bin/parker machine start
# Needs Go and the Xcode command line tools (xcode-select --install).
set -eu
cd "$(dirname "$0")/../.."
[ "$(uname -s)" = Darwin ] || { echo "build.sh: run this on macOS" >&2; exit 1; }
arch=$(go env GOARCH)
mkdir -p bin
CGO_ENABLED=1 go build -o bin/parker ./cmd/parker
codesign --force --sign - --entitlements hack/machine/parker.entitlements bin/parker
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o bin/parker-linux ./cmd/parker
codesign -d --entitlements - bin/parker 2>/dev/null | grep -q com.apple.security.virtualization ||
  { echo "build.sh: entitlement missing after codesign" >&2; exit 1; }
echo "built bin/parker (signed) and bin/parker-linux ($arch)"
