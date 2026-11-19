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
echo "--- selectors"
$P exec "$F" "${first%%-*}/leader" -- sh -c 'echo "leader is $PARKER_NODE"' </dev/null
if grep "federation:" "$F" >/dev/null; then
  echo "--- WAN federation"
  srv=$($P status "$F" | awk '$1 ~ /-server-/ && !n++ {print $1}')
  $P exec "$F" "$srv" -- consul members -wan </dev/null
  for dc in $(awk '/^    dc[0-9]+:/ {sub(":","",$1); print $1}' "$F"); do
    $P exec "$F" "$first" -- consul catalog nodes -datacenter "$dc" </dev/null >/dev/null && echo "reached $dc"
  done
fi
if grep "acl: true" "$F" >/dev/null; then
  echo "--- ACLs"
  if $P exec "$F" "$first" -- env CONSUL_HTTP_TOKEN= consul members </dev/null >/dev/null 2>&1; then
    echo "anonymous request was allowed" >&2; exit 1
  fi
  echo "anonymous denied"
  $P exec "$F" "$first" -- consul catalog services </dev/null
  $P exec "$F" "$first" -- consul keyring -list </dev/null | grep -q '\[' && echo "gossip key installed"
fi
if grep "config_entries:" "$F" >/dev/null; then
  echo "--- config entries"
  s1=${first%%-*}-server-1   # config entries run on the dc's first server
  $P logs "$F" "$s1/config-entries" -n 20 | grep applied
  $P exec "$F" "$s1" -- consul config list -kind service-defaults </dev/null
fi
if grep "tls: true" "$F" >/dev/null; then
  echo "--- TLS"
  if $P exec "$F" "$first" -- curl -sf http://127.0.0.1:8500/v1/status/leader </dev/null >/dev/null 2>&1; then
    echo "plain HTTP API is open" >&2; exit 1
  fi
  echo "plain HTTP closed"
  client=$($P status "$F" | awk 'NR>2 && $1 !~ /-server-/ && $3=="consul" && !n++ {print $1}')
  if [ -n "$client" ]; then
    $P exec "$F" "$client" -- sh -c 'curl -sf --cacert "$CONSUL_CACERT" "$CONSUL_HTTP_ADDR/v1/agent/self" -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" >/dev/null' </dev/null
    echo "client HTTPS ($client, auto-encrypt cert) verifies against the lab CA"
  fi
fi
if grep "^scenario:" "$F" >/dev/null; then
  echo "--- scenario"
  $P run --no-up "$F"
  if grep "^scenarios:" "$F" >/dev/null && grep "wrong-peers:" "$F" >/dev/null; then
    if $P run --no-up "$F" wrong-peers >/dev/null 2>&1; then
      echo "a failing scenario exited 0" >&2; exit 1
    fi
    echo "failing scenario exits non-zero"
  fi
fi
if grep "^name: faults" "$F" >/dev/null; then
  echo "--- parker fault"
  web_state() { $P status "$F" | awk '$1=="dc1-web" && $3=="consul" {s=$5} END {print s}'; }
  $P fault "$F" stop dc1/web
  [ "$(web_state)" = stopped ] || { echo "after stop: $(web_state)" >&2; exit 1; }
  $P fault "$F" start dc1/web
  [ "$(web_state)" = ready ] || { echo "after start: $(web_state)" >&2; exit 1; }
  $P fault "$F" pause dc1/web
  [ "$(web_state)" = paused ] || { echo "after pause: $(web_state)" >&2; exit 1; }
  $P fault "$F" resume dc1/web
  srv=$($P status "$F" | awk '$1=="dc1-server-1" && !n++ {print $2}')
  leader() { $P exec "$F" dc1/web -- curl -sf -m 2 "http://$srv:8500/v1/status/leader" </dev/null >/dev/null 2>&1; }
  leader || { echo "web can't reach $srv before the partition" >&2; exit 1; }
  $P fault "$F" partition dc1/web
  if leader; then echo "web still reaches $srv after the partition" >&2; exit 1; fi
  $P fault "$F" heal
  leader || { echo "web can't reach $srv after heal" >&2; exit 1; }
  if $P fault "$F" explode dc1 >/dev/null 2>&1; then echo "unknown fault exited 0" >&2; exit 1; fi
  echo ok
fi
if grep -E "^versions:.*envoy" "$F" >/dev/null; then
  echo "--- pinned envoy on exec PATH"
  $P exec "$F" "$first" -- envoy --version </dev/null | grep -o "/1\.[0-9]*\.[0-9]*/"
fi
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
scheme=http port=8500 cacert=
if grep "tls: true" "$F" >/dev/null; then
  # HTTPS only; the server certificates carry the proxy names too (a
  # client's auto_encrypt certificate doesn't), so go to a server.
  first=$($P status "$F" | awk '$1 ~ /-server-/ && !n++ {print $1}')
  short=${first#*-}; dc=${first%%-*}
  scheme=https port=8501 cacert="--cacert $PARKER_RUN_DIR/$lab/nodes/$first/ca.pem"
  kill $pf; wait $pf 2>/dev/null || true
  $P port-forward "$F" "$first" 18500:8501 </dev/null >/dev/null 2>&1 &
  pf=$!
  sleep 1
fi
curl -sf $cacert --proxy "socks5h://127.0.0.1:1080" "$scheme://$short.$dc.$lab.parker:$port/v1/status/leader" | grep 8300 >/dev/null
curl -sf $cacert --proxy "http://127.0.0.1:1080" "$scheme://$first.$lab.parker:$port/v1/status/leader" | grep 8300 >/dev/null
curl -sf $cacert "$scheme://127.0.0.1:18500/v1/status/leader" | grep 8300 >/dev/null
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
