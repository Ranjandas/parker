#!/bin/bash
# Enterprise end-to-end test. It needs a Consul Enterprise license, so it
# runs where one is (not in CI for now) and skips cleanly without one:
#
#   CONSUL_LICENSE_PATH=~/consul.hclic hack/ent-e2e.sh [FILE]
#
# Like hack/rootless-e2e.sh it runs inside a private user + network + mount
# namespace, so it needs no sudo and leaves nothing behind. It checks that
# the lab runs Enterprise with the license, that `up` refuses without one,
# and that the license never lands in expand output, the run dir (except
# its one 0600 file) or logs, and is gone after down.
set -euo pipefail
cd "$(dirname "$0")/.."
F=${1:-examples/enterprise.yaml}
if [ -n "${CONSUL_LICENSE_PATH:-}" ]; then
  LIC=$(cat "$CONSUL_LICENSE_PATH")
elif [ -n "${CONSUL_LICENSE:-}" ]; then
  LIC=$CONSUL_LICENSE
else
  echo "SKIP: no Consul Enterprise license (set CONSUL_LICENSE_PATH or CONSUL_LICENSE)"
  exit 0
fi
# A slice of the license to search for; the whole thing is one long line.
export NEEDLE=${LIC:20:40}
[ ${#NEEDLE} = 40 ] || { echo "the license looks too short" >&2; exit 1; }
make -s build
./bin/parker fetch "$F" >/dev/null

echo "--- up without a license is refused"
if out=$(env -u CONSUL_LICENSE -u CONSUL_LICENSE_PATH ./bin/parker up --no-daemon "$F" 2>&1); then
  echo "up worked without a license" >&2; exit 1
fi
case "$out" in *"needs a Consul Enterprise license"*) echo "refused: ${out%%$'\n'*}" ;; *) echo "unexpected: $out" >&2; exit 1 ;; esac

echo "--- expand output carries no license"
if ./bin/parker expand "$F" | grep -qF "$NEEDLE"; then echo "license in expand output" >&2; exit 1; fi

exec unshare -Urnm --propagation private bash -s "$F" <<'INNER'
set -euo pipefail
F=$1
P=./bin/parker
mount -t tmpfs tmpfs /run && mkdir -p /run/netns
export PARKER_RUN_DIR=/run/parker
ip link set lo up
lab=$($P expand "$F" | awk '/^name:/ {print $2; exit}')

$P up "$F"
# A node with a Consul agent: the first server (an external service's
# node, e.g. api, sorts first but runs no agent).
first=$($P status "$F" | awk 'NR>2 && $1 ~ /-server-1$/ {print $1; exit}')
[ -n "$first" ] || first=$($P status "$F" | awk 'NR==3 {print $1}')
echo "--- Enterprise is running, licensed"
$P exec "$F" "$first" -- consul version </dev/null | grep -F +ent
$P exec "$F" "$first" -- consul license get </dev/null | grep -iE 'valid|expires' | head -3

echo "--- the license is in one 0600 file, nowhere else"
lic=/run/parker/$lab/consul.hclic
[ "$(stat -c %a "$lic")" = 600 ] || { echo "license file mode $(stat -c %a "$lic")" >&2; exit 1; }
if grep -rlF "$NEEDLE" /run/parker | grep -vxF "$lic"; then echo "license leaked into the files above" >&2; exit 1; fi
if $P describe "$F" "$first" | grep -qF "$NEEDLE"; then echo "license in describe" >&2; exit 1; fi
echo "only $lic"

if grep "^scenario:" "$F" >/dev/null; then
  echo "--- scenario"
  $P run --no-up "$F"
fi

echo "--- down removes it"
$P down "$F"
[ ! -e "$lic" ] || { echo "license left after down" >&2; exit 1; }
$P daemon stop
echo PASS
INNER
