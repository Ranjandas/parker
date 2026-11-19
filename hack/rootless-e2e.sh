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
./bin/parker fetch hack/e2e/restart.yaml >/dev/null

exec unshare -Urnm --propagation private bash -s "$F" <<'INNER'
set -euo pipefail
F=$1
P=./bin/parker
mount -t tmpfs tmpfs /run && mkdir -p /run/netns
export PARKER_RUN_DIR=/run/parker
# The run dir lives on a private tmpfs; copy it out on failure so CI can
# keep the logs (PARKER_E2E_LOGS, e.g. /tmp/parker-e2e-logs).
if [ -n "${PARKER_E2E_LOGS:-}" ]; then
  trap 'rc=$?; [ $rc = 0 ] || { mkdir -p "$PARKER_E2E_LOGS"; cp -r /run/parker/. "$PARKER_E2E_LOGS"/ 2>/dev/null || true; }' EXIT
fi
ip link set lo up

$P up "$F"
$P daemon status
echo "--- status"
$P status "$F"
first=$($P status "$F" | awk 'NR==3 {print $1}')
echo "--- consul members (from $first)"
$P exec "$F" "$first" -- consul members
echo "--- describe"
$P describe "$F" "$first" | grep "consul.json" >/dev/null && echo ok
echo "--- logs"
$P logs "$F" "$first" -n 3
echo "--- re-up is idempotent"
$P up "$F" >/dev/null && echo ok

echo "--- restart policy"
R=hack/e2e/restart.yaml
$P up "$R" >/dev/null
pid=$($P status "$R" | awk '$1=="sleeper" {print $4}')
kill "$pid"
for _ in $(seq 50); do
  line=$($P status "$R" | awk '$1=="sleeper"')
  set -- $line
  [ "${6:-0}" = 1 ] && [ "$5" = ready ] && [ "$4" != "$pid" ] && break
  sleep 0.2
done
echo "$line"
[ "${6:-0}" = 1 ] || { echo "process was not restarted"; exit 1; }
$P down "$R"

echo "--- exec through the daemon API (unix socket)"
# </dev/null: remote exec forwards stdin, and this script is bash's stdin.
export PARKER_HOST=unix:///run/parker/parker.sock
$P exec -T "$F" "$first" -- consul members </dev/null | grep alive >/dev/null
[ "$(echo hello | $P exec -T "$F" "$first" -- cat)" = hello ]
rc=0; $P exec -T "$F" "$first" -- sh -c 'exit 3' </dev/null || rc=$?
[ "$rc" = 3 ] || { echo "exit code not propagated: $rc"; exit 1; }
unset PARKER_HOST
echo ok

echo "--- proxy and port-forward"
lab=$($P status "$F" | awk 'NR==1 {sub(":", "", $2); print $2}')
short=${first#*-}; dc=${first%%-*}   # dc1-server-1 -> server-1, dc1
$P proxy --listen 127.0.0.1:1080 </dev/null >/dev/null 2>&1 &
pp=$!
$P port-forward "$F" "$first" 18500:8500 </dev/null >/dev/null 2>&1 &
pf=$!
sleep 1
curl -sf --proxy "socks5h://127.0.0.1:1080" "http://$short.$dc.$lab.parker:8500/v1/status/leader" | grep 8300 >/dev/null
curl -sf --proxy "http://127.0.0.1:1080" "http://$first.$lab.parker:8500/v1/status/leader" | grep 8300 >/dev/null
curl -sf "http://127.0.0.1:18500/v1/status/leader" | grep 8300 >/dev/null
code=$(curl -s -o /dev/null -w '%{http_code}' --proxy "http://127.0.0.1:1080" http://192.0.2.1:80/ || true)
[ "$code" = 403 ] || { echo "proxy reached a non-lab address ($code)"; exit 1; }
kill $pp $pf
echo ok

echo "--- down, twice"
$P down "$F"
$P down "$F"
echo "--- down works without the daemon"
$P up "$F" >/dev/null
$P daemon stop
$P status "$F" | sed -n 1p  # sed, not head: head exits early and SIGPIPE fails pipefail
$P down "$F"

echo "--- remote client over TLS + token"
export PARKER_STATE_DIR=/run/parker-state
$P daemon --listen 127.0.0.1:7443 >/run/parker/remote-daemon.log 2>&1 &
for _ in $(seq 50); do [ -f "$PARKER_STATE_DIR/tls/cert.pem" ] && break; sleep 0.1; done
eval "$($P daemon credentials | grep '^export PARKER_TOKEN')"
export PARKER_HOST=https://127.0.0.1:7443 PARKER_TLS_CA=$PARKER_STATE_DIR/tls/cert.pem
$P up "$F" >/dev/null
$P status "$F" | grep ready >/dev/null
$P exec -T "$F" "$first" -- consul members </dev/null | grep alive >/dev/null
out=$(PARKER_TOKEN=wrong $P status "$F" 2>&1 || true)
case "$out" in *"wrong parker token"*) echo "wrong token rejected" ;; *) echo "wrong token accepted: $out"; exit 1 ;; esac
$P down "$F"
$P daemon stop
unset PARKER_HOST PARKER_TOKEN PARKER_TLS_CA
left_ns=$(ls /run/netns | wc -l)
left_links=$(ip -o link | grep -c ' pk-\| pk[0-9a-f]' || true)
echo "leftover netns=$left_ns links=$left_links"
[ "$left_ns" = 0 ] && [ "$left_links" = 0 ]
echo PASS
INNER
