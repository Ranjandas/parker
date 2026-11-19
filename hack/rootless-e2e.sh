#!/bin/bash
# End-to-end test without sudo: runs parker inside a private user + network +
# mount namespace (unshare -Urnm), where we hold CAP_NET_ADMIN over a
# throwaway network stack. The lab is invisible to the host and vanishes
# when the script exits. Binaries must already be cached (no network
# inside): run `bin/parker fetch FILE` first.
#
#   hack/rootless-e2e.sh [FILE]
set -euo pipefail
cd "$(dirname "$0")/.."
F=${1:-examples/minimal.yaml}
./bin/parker fetch "$F" >/dev/null

exec unshare -Urnm --propagation private bash -s "$F" <<'INNER'
set -euo pipefail
F=$1
P=./bin/parker
mount -t tmpfs tmpfs /run && mkdir -p /run/netns
export PARKER_RUN_DIR=/run/parker
ip link set lo up

$P up "$F"
first=$($P status "$F" | head -1)
echo "--- consul members (from $first)"
$P exec "$F" "$first" -- consul members
echo "--- re-up is idempotent"
$P up "$F" >/dev/null && echo ok
echo "--- down, twice"
$P down "$F"
$P down "$F"
left_ns=$(ls /run/netns | wc -l)
left_links=$(ip -o link | grep -c ' pk-\| pk[0-9a-f]' || true)
echo "leftover netns=$left_ns links=$left_links"
[ "$left_ns" = 0 ] && [ "$left_links" = 0 ]
echo PASS
INNER
