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
first=$($P status "$F" | awk 'NR>2 && $3=="consul" && !n++ {print $1}')   # a node with an agent
if [ -z "$first" ] && grep "^vault:" "$F" >/dev/null; then
  # A Vault-only lab: its own checks, then the scenario and a re-up.
  v1=$($P status "$F" | awk 'NR>2 && $3=="vault" && !n++ {print $1}')
  echo "--- vault status (from $v1)"
  $P exec "$F" "$v1" -- vault status </dev/null
  for n in $($P status "$F" | awk 'NR>2 && $3=="vault" {print $1}'); do
    $P exec "$F" "$n" -- vault status </dev/null | grep -E "^Sealed +false" >/dev/null || { echo "$n is sealed" >&2; exit 1; }
  done
  echo "every server unsealed"
  $P exec "$F" "$v1" -- sh -c 'vault token lookup >/dev/null && echo "root token works"' </dev/null
  if grep "^scenario:" "$F" >/dev/null; then
    echo "--- scenario"
    $P run --no-up "$F"
  fi
  echo "--- re-up is idempotent"
  $P up "$F" >/dev/null && echo ok
  echo "--- down and resume: the cluster unseals again, its data kept"
  lab=$($P status "$F" | awk 'NR==1 {sub(":", "", $2); print $2}')
  $P exec "$F" "$v1" -- vault secrets enable -path=kept kv </dev/null >/dev/null
  $P down "$lab" >/dev/null
  $P up "$lab" >/dev/null
  $P exec "$lab" "$v1" -- vault status </dev/null | grep -E "^Sealed +false" >/dev/null
  $P exec "$lab" "$v1" -- vault secrets list </dev/null | grep "^kept/" >/dev/null
  echo "resumed, unsealed, data kept"
  $P down "$lab"
  echo PASS
  exit 0
fi
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
  # A log level change reloads the agent with the management token.
  $P logs "$F" "$first" --level debug --only consul | grep -F "$first/consul (consul): debug" >/dev/null
  $P logs "$F" "$first" --level reset --only consul | grep -F "(consul): info" >/dev/null
  echo "agent log level changed and reset under ACLs"
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
if grep "^name: fake-chain" "$F" >/dev/null; then
  echo "--- parker fake"
  $P fake "$F" dc1/api --error-rate 1 --error-code 502 | grep "errors 100% (502)"
  code=$($P exec "$F" dc1-web -- curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/ </dev/null)
  [ "$code" = 500 ] || { echo "web answered $code with api failing" >&2; exit 1; }
  $P fake "$F" dc1/api | grep "errors 100%"
  $P fake "$F" dc1/api --reset | grep "errors off"
  $P exec "$F" dc1-web -- sh -c 'curl -s -w "\n%{http_code}\n" http://127.0.0.1:8080/ | tee /tmp/web.out | tail -1 | grep -x 200 || { cat /tmp/web.out; exit 1; }' </dev/null
  echo "faults changed at runtime and reset"
  echo "--- parker call"
  $P call "$F" dc1/web | tee /dev/stderr | grep "└─ db  dc1-db · dc1  200" >/dev/null
  $P call "$F" dc1/web --json | grep '"fact":' >/dev/null
  $P call "$F" dc1/web --every 100ms --count 3 | grep -- "--- 3 requests, 0 failed" >/dev/null
  if $P call "$F" dc1/web -H 'X-Parker-Fault: db=error-rate:100' >/dev/null; then echo "call with a fault at db passed" >&2; exit 1; fi
  echo "--- fake /ui through the daemon's app proxy"
  sock=$PARKER_RUN_DIR/parker.sock
  app="http://parker/app/fake-chain/dc1-db/http/7000"
  curl -sf --unix-socket "$sock" "$app/ui" | grep "Fake service" >/dev/null
  # api sits behind a transparent proxy: its page is reached on the node's loopback.
  curl -sf --unix-socket "$sock" "http://parker/app/fake-chain/dc1-api/http/9090/ui" | grep "Fake service" >/dev/null
  curl -sf --unix-socket "$sock" -H 'Content-Type: application/json' -d '{"health":{"code":503}}' "$app/.parker/fake/faults" | grep '"injected":true' >/dev/null
  $P fake "$F" dc1/db | grep "health 503" >/dev/null
  curl -sf --unix-socket "$sock" -X POST "$app/.parker/fake/reset" | grep '"injected":false' >/dev/null
  code=$($P exec "$F" dc1-db -- curl -s -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' -d '{"health":{"code":503}}' http://127.0.0.1:7000/.parker/fake/faults </dev/null)
  [ "$code" = 403 ] || { echo "the service port took a fault change ($code)" >&2; exit 1; }
  echo "parker call and the fake UI work"
  echo "--- log levels at runtime"
  $P logs "$F" dc1-web --level debug | tee /tmp/ll.out
  for want in "dc1-web/consul (consul): debug" "dc1-web/web-sidecar (envoy): debug" "dc1-web/web (fake): debug"; do
    grep -F "$want" /tmp/ll.out >/dev/null || { echo "missing: $want" >&2; exit 1; }
  done
  $P logs "$F" dc1-web/web-sidecar --logger upstream=trace | grep -F "upstream: trace" >/dev/null
  $P exec "$F" dc1-web -- curl -sf -o /dev/null http://127.0.0.1:8080/ </dev/null
  $P logs "$F" dc1-web/web -n 100 | grep -E "\[DEBUG\] request id=.* upstream " >/dev/null
  # The reloaded agent logs at debug.
  for i in $(seq 1 20); do
    $P logs "$F" dc1-web/consul -n 300 | grep -F "[DEBUG]" >/dev/null && break
    [ "$i" = 20 ] && { echo "consul logged no [DEBUG] line after the reload" >&2; exit 1; }
    sleep 0.5
  done
  $P logs "$F" dc1 --level reset | tee /tmp/ll.out
  if grep -E ": (debug|trace)" /tmp/ll.out >/dev/null; then echo "reset left a debug level" >&2; exit 1; fi
  if $P logs "$F" dc1-web --level loud >/dev/null 2>&1; then echo "a bad level exited 0" >&2; exit 1; fi
  echo "log levels changed at runtime and reset"
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

echo "--- web UI through parker ui (local socket)"
$P ui "$F" --no-open --listen 127.0.0.1:18080 >/run/parker/ui.out 2>&1 </dev/null &
up=$!
for _ in $(seq 50); do grep -q '^open: ' /run/parker/ui.out && break; sleep 0.1; done
link=$(sed -n 's/^open: //p' /run/parker/ui.out)
tok=$(echo "$link" | sed -n 's/.*[#&]token=\([0-9a-f]*\).*/\1/p')
[ -n "$tok" ] || { echo "no sign-in link: $(cat /run/parker/ui.out)"; exit 1; }
curl -sf http://127.0.0.1:18080/ | grep app.js >/dev/null
jar=/run/parker/ui.jar
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:18080/ui/api/parker.v1.ParkerService/ListLabs)
[ "$code" = 401 ] || { echo "UI API without a session: $code"; exit 1; }
curl -sf -c $jar -H 'Content-Type: application/json' -d "{\"token\":\"$tok\"}" http://127.0.0.1:18080/ui/session
curl -sf -b $jar -H 'Content-Type: application/json' -d "{\"lab\":\"$lab\"}" \
  http://127.0.0.1:18080/ui/api/parker.v1.ParkerService/Status | grep "\"$first\"" >/dev/null
# The topology view's graph names the same nodes.
curl -sf -b $jar -H 'Content-Type: application/json' -d "{\"lab\":\"$lab\"}" \
  http://127.0.0.1:18080/ui/api/parker.v1.ParkerService/Graph | grep -E "\"name\": ?\"$first\"" >/dev/null
# An app inside the lab (Consul's HTTP API on $first) opens on an origin of
# its own: the link's one-time ticket becomes that origin's cookie.
open=$(curl -sf -b $jar -H 'Content-Type: application/json' \
  -d "{\"lab\":\"$lab\",\"node\":\"$first\",\"port\":$port,\"scheme\":\"$scheme\",\"path\":\"/v1/status/leader\"}" \
  http://127.0.0.1:18080/ui/open | sed -n 's/.*"url": *"\([^"]*\)".*/\1/p')
apphost=$(echo "$open" | sed -n 's#^http://\([^:/]*\):.*#\1#p')
[ -n "$apphost" ] || { echo "no app link: $open"; exit 1; }
curl -sf -L -b /run/parker/app.jar -c /run/parker/app.jar --resolve "$apphost:18080:127.0.0.1" "$open" | grep 8300 >/dev/null
code=$(curl -s -o /dev/null -w '%{http_code}' --resolve "$apphost:18080:127.0.0.1" "http://$apphost:18080/v1/status/leader")
[ "$code" = 401 ] || { echo "app origin without its cookie: $code"; exit 1; }
code=$(curl -s -o /dev/null -w '%{http_code}' -b $jar -X POST -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:18080/ui/api/parker.v1.ParkerService/Exec)
[ "$code" = 404 ] || { echo "UI allowed Exec: $code"; exit 1; }
kill $up; wait $up 2>/dev/null || true
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
echo "--- web UI on the daemon's TLS listener"
jar=/run/parker/ui-remote.jar
curl -sf --cacert "$PARKER_TLS_CA" https://127.0.0.1:7443/ | grep app.js >/dev/null
curl -sf --cacert "$PARKER_TLS_CA" -c $jar -H 'Content-Type: application/json' -d "{\"token\":\"$PARKER_TOKEN\"}" https://127.0.0.1:7443/ui/session
curl -sf --cacert "$PARKER_TLS_CA" -b $jar -H 'Content-Type: application/json' -d '{}' \
  https://127.0.0.1:7443/ui/api/parker.v1.ParkerService/ListLabs | grep '"labs"' >/dev/null
echo ok
$P down "$F"
$P daemon stop
unset PARKER_HOST PARKER_TOKEN PARKER_TLS_CA
left_ns=$(ls /run/netns | wc -l)
left_links=$(ip -o link | grep -c ' pk-\| pk[0-9a-f]' || true)
echo "leftover netns=$left_ns links=$left_links"
[ "$left_ns" = 0 ] && [ "$left_links" = 0 ]

echo "--- status lists the downed lab until purge"
lab=$($P expand "$F" | awk '/^name:/ && !n++ {print $2}')
$P status | grep -E "^$lab +down" >/dev/null
$P down "$lab" --purge | grep "removed $PARKER_RUN_DIR/$lab" >/dev/null
[ ! -e "$PARKER_RUN_DIR/$lab" ] || { echo "run dir left after --purge"; exit 1; }
if $P status | grep -E "^$lab " >/dev/null; then echo "purged lab still listed"; exit 1; fi
$P prune -y >/dev/null
if $P status | grep -E " down " >/dev/null; then echo "prune left: $($P status)"; exit 1; fi
echo PASS
INNER
