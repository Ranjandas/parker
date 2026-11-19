#!/bin/bash
# End-to-end test of `--rootless` mode as it is really used: a normal user
# runs parker with no sudo and no unshare wrapper. parker starts a per-user
# daemon in its own user + network + mount namespace; everything else goes
# through the daemon's socket.
#
#   hack/rootless-mode-e2e.sh [FILE]     # must not run as root
set -euo pipefail
cd "$(dirname "$0")/.."
F=${1:-examples/minimal.yaml}
P=./bin/parker
if [ "$(id -u)" = 0 ]; then echo "run this as a normal user"; exit 1; fi
export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-$(mktemp -d)}
D=$XDG_RUNTIME_DIR/parker
cleanup() {
  rc=$?
  if [ $rc != 0 ] && [ -n "${PARKER_E2E_LOGS:-}" ]; then
    mkdir -p "$PARKER_E2E_LOGS/rootless"; cp -r "$D/." "$PARKER_E2E_LOGS/rootless/" 2>/dev/null || true
  fi
  $P daemon stop >/dev/null 2>&1 || true
}
trap cleanup EXIT

$P fetch "$F" >/dev/null
echo "--- up (starts a rootless daemon)"
$P up "$F"
$P daemon status
lab=$($P status "$F" | awk 'NR==1 {sub(":", "", $2); print $2}')
first=$($P status "$F" | awk 'NR==3 {print $1}')
$P status "$F" | awk 'NR>2 && $5 != "ready" {bad=1} END {exit bad}'
echo "--- exec (over the API: the namespaces are the daemon's)"
$P exec -T "$F" "$first" -- consul members </dev/null | grep alive >/dev/null
echo "--- logs"
$P logs "$F" "$first" -n 2
echo "--- proxy and port-forward (the only way in from the host)"
$P proxy --listen 127.0.0.1:1081 </dev/null >/dev/null 2>&1 &
pp=$!
$P port-forward "$F" "$first" 18501:8500 </dev/null >/dev/null 2>&1 &
pf=$!
sleep 1
curl -sf --proxy socks5h://127.0.0.1:1081 "http://$first.$lab.parker:8500/v1/status/leader" | grep 8300 >/dev/null
curl -sf http://127.0.0.1:18501/v1/status/leader | grep 8300 >/dev/null
kill $pp $pf
echo "--- host is untouched"
if ip -o link | grep ' pk-' >/dev/null; then echo "bridge leaked onto the host"; exit 1; fi
echo "--- down, daemon stop"
$P down "$F"
$P daemon stop
sleep 0.5
if pgrep -u "$(id -u)" -f "consul agent -config-file=$D" >/dev/null; then echo "consul left running"; exit 1; fi
echo PASS
