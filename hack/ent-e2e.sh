#!/bin/bash
# Enterprise end-to-end test. It needs the Enterprise licenses the lab
# runs (Consul's, Vault's, or both), so it runs where they are (not in CI
# for now) and skips cleanly without them:
#
#   CONSUL_LICENSE_PATH=~/consul.hclic hack/ent-e2e.sh [FILE]
#   VAULT_LICENSE_PATH=~/vault.hclic CONSUL_LICENSE_PATH=~/consul.hclic \
#     hack/ent-e2e.sh examples/vault-enterprise.yaml
#
# Like hack/rootless-e2e.sh it runs inside a private user + network + mount
# namespace, so it needs no sudo and leaves nothing behind. It checks that
# the lab runs Enterprise with the licenses, that `up` refuses without one,
# that each license reaches its own product's processes only, and that no
# license lands in expand output, the run dir (except its one 0600 file)
# or logs, and that they're gone after down.
set -euo pipefail
cd "$(dirname "$0")/.."
F=${1:-examples/enterprise.yaml}

# The products the lab runs as Enterprise builds.
products=()
for p in consul vault; do
  grep -Eq "(^|[ {,])$p: *[0-9][0-9.]*\+ent" "$F" && products+=("$p")
done
[ ${#products[@]} -gt 0 ] || { echo "$F runs no Enterprise build" >&2; exit 1; }
needles=()
for p in "${products[@]}"; do
  P=${p^^}
  path_var=${P}_LICENSE_PATH lic_var=${P}_LICENSE
  if [ -n "${!path_var:-}" ]; then
    LIC=$(cat "${!path_var}")
  elif [ -n "${!lic_var:-}" ]; then
    LIC=${!lic_var}
  else
    echo "SKIP: no ${p^} Enterprise license (set $path_var or $lic_var)"
    exit 0
  fi
  # A slice of the license to search for; the whole thing is one long line.
  n=${LIC:20:40}
  [ ${#n} = 40 ] || { echo "the $p license looks too short" >&2; exit 1; }
  needles+=("$n")
done
export PRODUCTS="${products[*]}" NEEDLES="${needles[*]}"
make -s build
./bin/parker fetch "$F" >/dev/null

for p in "${products[@]}"; do
  P=${p^^}
  echo "--- up without a ${p^} license is refused"
  if out=$(env -u "${P}_LICENSE" -u "${P}_LICENSE_PATH" ./bin/parker up --no-daemon "$F" 2>&1); then
    echo "up worked without a $p license" >&2; exit 1
  fi
  case "$out" in *"needs a ${p^} Enterprise license"*) echo "refused: ${out%%$'\n'*}" ;; *) echo "unexpected: $out" >&2; exit 1 ;; esac
done

echo "--- expand output carries no license"
exp=$(./bin/parker expand "$F")
for n in $NEEDLES; do
  if grep -F "$n" <<<"$exp" >/dev/null; then echo "license in expand output" >&2; exit 1; fi
done

exec unshare -Urnm --propagation private bash -s "$F" <<'INNER'
set -euo pipefail
trap 'echo "FAIL: line $LINENO: $BASH_COMMAND" >&2' ERR
F=$1
P=./bin/parker
mount -t tmpfs tmpfs /run && mkdir -p /run/netns
export PARKER_RUN_DIR=/run/parker
ip link set lo up
lab=$($P expand "$F" | awk '/^name:/ && !n++ {print $2}')
has() { [[ " $PRODUCTS " == *" $1 "* ]]; }

$P up "$F"
# awk reads all of the status output: exiting early would SIGPIPE status,
# and pipefail plus set -e would end the script without a word.
st=$($P status "$F")
if has consul; then
  # A node with a Consul agent: the first server (an external service's
  # node, e.g. api, sorts first but runs no agent).
  first=$(awk 'NR>2 && $1 ~ /-server-1$/ && !n++ {print $1}' <<<"$st")
  [ -n "$first" ] || first=$(awk 'NR>2 && $3=="consul" && !n++ {print $1}' <<<"$st")
  echo "--- Consul Enterprise is running, licensed"
  $P exec "$F" "$first" -- consul version </dev/null | grep -F +ent
  lic_info=$($P exec "$F" "$first" -- consul license get </dev/null)
  grep -iE 'valid|expires' <<<"$lic_info" | sed -n 1,3p | grep . || { echo "license get: $lic_info" >&2; exit 1; }
fi
if has vault; then
  # A server that answers: a replication primary's (a DR secondary
  # refuses requests), else the first.
  v1=$(awk 'NR>2 && $3=="vault-replication" && !n++ {print $1}' <<<"$st")
  [ -n "$v1" ] || v1=$(awk 'NR>2 && $3=="vault" && !n++ {print $1}' <<<"$st")
  echo "--- Vault Enterprise is running, licensed"
  $P exec "$F" "$v1" -- vault version </dev/null | grep -F +ent
  lic_info=$($P exec "$F" "$v1" -- vault read -format=json sys/license/status </dev/null)
  grep -E '"(expiration_time|license_id)"' <<<"$lic_info" | sed -n 1,2p | grep . || { echo "license status: $lic_info" >&2; exit 1; }
  # Standbys are performance standbys when the license has the feature.
  servers=$(awk 'NR>2 && $3=="vault"' <<<"$st" | wc -l)
  if [ "$servers" -gt 1 ]; then
    for i in $(seq 15); do
      $P status "$F" | grep -E " vault .* perf-standby" >/dev/null && break
      sleep 2
    done
    $P status "$F" | grep -E " vault .* perf-standby" || { $P status "$F" | grep -E " vault "; echo "note: no perf-standby (does the license include performance standbys?)"; }
  fi
fi

echo "--- each license is in one 0600 file, nowhere else"
files=()
for p in $PRODUCTS; do
  lic=/run/parker/$lab/$p.hclic
  [ "$(stat -c %a "$lic")" = 600 ] || { echo "$lic mode $(stat -c %a "$lic")" >&2; exit 1; }
  files+=("$lic")
done
for n in $NEEDLES; do
  if grep -rlF "$n" /run/parker | grep -vxF -f <(printf '%s\n' "${files[@]}"); then echo "license leaked into the files above" >&2; exit 1; fi
  for node in $(awk 'NR>2 {print $1}' <<<"$st" | sort -u); do
    if $P describe "$F" "$node" | grep -F "$n" >/dev/null; then echo "license in describe $node" >&2; exit 1; fi
  done
done
echo "only ${files[*]}"

echo "--- each product's processes get their own license only"
checked=0
for pid in $(pgrep -f .); do
  env=$(tr '\0' '\n' 2>/dev/null </proc/$pid/environ) || continue
  grep -qx "PARKER_LAB=$lab" <<<"$env" || continue
  exe=$(basename "$(readlink /proc/$pid/exe 2>/dev/null)")
  case "$exe" in
    vault) want=VAULT_LICENSE_PATH not=CONSUL_LICENSE_PATH ;;
    *) want=CONSUL_LICENSE_PATH not=VAULT_LICENSE_PATH ;;
  esac
  if grep -q "^$not=" <<<"$env"; then echo "$exe (pid $pid) has $not" >&2; exit 1; fi
  prod=${want%%_*}; prod=${prod,,}
  if has "$prod" && [ "$exe" = "$prod" ] && ! grep -q "^$want=" <<<"$env"; then echo "$exe (pid $pid) lacks $want" >&2; exit 1; fi
  checked=$((checked + 1))
done
[ "$checked" -gt 0 ] || { echo "found none of the lab's processes" >&2; exit 1; }
echo "ok ($checked processes)"

if grep "^scenario:" "$F" >/dev/null; then
  echo "--- scenario"
  if ! $P run --no-up "$F"; then
    # The lab dies with this namespace, so look at it now.
    out=$(P=$P hack/lab-diag.sh "$F" 2>&1 || true)
    for n in $NEEDLES; do out=$(grep -vF "$n" <<<"$out" || true); done
    echo "$out"
    $P down "$F" >/dev/null 2>&1 || true; $P daemon stop >/dev/null 2>&1 || true
    echo "FAIL: scenario (diagnostics above)" >&2; exit 1
  fi
fi

echo "--- down removes them"
$P down "$F"
for f in "${files[@]}"; do
  [ ! -e "$f" ] || { echo "$f left after down" >&2; exit 1; }
done
$P daemon stop
echo PASS
INNER
