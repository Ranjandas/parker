#!/bin/bash
# Print what a running lab's mesh looks like, for a bug report or a failed
# scenario: every node's processes, each Envoy's recent log and cluster
# health, and from each datacenter's first server the health checks and
# gateway/resolver config entries of every partition and namespace.
#
#   hack/lab-diag.sh FILE
#
# hack/ent-e2e.sh runs it when the scenario fails. It only reads.
set -uo pipefail
cd "$(dirname "$0")/.."
F=$1
P=${P:-./bin/parker}

st=$($P status "$F") || exit 1
echo "=== status"
echo "$st"

# Envoys: sidecars and gateways, NODE/PROCESS.
envoys=$(awk 'NR>2 && ($3 ~ /-sidecar$/ || $3 ~ /gateway$/) {print $1 "/" $3}' <<<"$st" | sort -u)
for np in $envoys; do
  echo "=== $np: last log lines"
  $P logs "$F" "$np" -n 40 </dev/null 2>&1
  echo "=== $np: Envoy clusters"
  # The admin port is the node's 127.0.0.1:190xx listener.
  $P exec "$F" "${np%/*}" -- sh -c '
    for p in $(ss -Hltn | awk "{print \$4}" | sed -n "s/^127\.0\.0\.1:\(190[0-9][0-9]\)$/\1/p"); do
      curl -s -m 2 "127.0.0.1:$p/clusters" | grep -E "::(health_flags|cx_connect_fail|cx_total|rq_error|rq_total)::" | grep -v "::0$"
    done' </dev/null 2>&1
done

# From each datacenter's first server (its exec env has the API address,
# CA and management token).
read -r -d '' PY <<'EOF'
import json, os, ssl, urllib.request, urllib.error
addr = os.environ.get("CONSUL_HTTP_ADDR", "http://127.0.0.1:8500")
if "://" not in addr:
    addr = "http://" + addr
ctx = ssl.create_default_context(cafile=os.environ.get("CONSUL_CACERT") or None)
def get(path):
    req = urllib.request.Request(addr + path, headers={"X-Consul-Token": os.environ.get("CONSUL_HTTP_TOKEN", "")})
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=5) as r:
            return json.load(r)
    except urllib.error.HTTPError as e:
        return None if e.code in (404, 405) else "HTTP %d %s" % (e.code, e.read().decode(errors="replace").strip())
    except Exception as e:
        return "error: %s" % e
parts = get("/v1/partitions")
ent = isinstance(parts, list)  # CE has no partitions or namespaces
parts = [p["Name"] for p in parts] if ent else ["default"]
for part in parts:
    nss = get("/v1/namespaces?partition=" + part) if ent else None
    nss = [n["Name"] for n in nss] if isinstance(nss, list) else ["default"]
    for ns in nss:
        q = "partition=%s&ns=%s" % (part, ns) if ent else ""
        checks = get("/v1/health/state/any?" + q)
        if isinstance(checks, list) and checks:
            print("--- checks in %s/%s" % (part, ns))
            for c in checks:
                print("  %-9s node=%s service=%s check=%s %s" % (c["Status"], c["Node"], c.get("ServiceName") or "-", c["CheckID"], (c.get("Output") or "").strip()[:120]))
        elif not isinstance(checks, list):
            print("--- checks in %s/%s: %s" % (part, ns, checks))
        for kind in ("terminating-gateway", "service-resolver", "service-intentions", "exported-services", "proxy-defaults", "mesh"):
            es = get("/v1/config/%s?%s" % (kind, q))
            if isinstance(es, list):
                for e in es:
                    # Partition-wide kinds list once, under default.
                    if e.get("Namespace", "default") != ns:
                        continue
                    for k in ("CreateIndex", "ModifyIndex", "Hash"):
                        e.pop(k, None)
                    print("--- %s %s in %s/%s: %s" % (kind, e.get("Name"), part, ns, json.dumps(e, sort_keys=True)))
for part in parts:
    ps = get("/v1/peerings" + ("?partition=" + part if ent else ""))
    if isinstance(ps, list):
        for p in ps:
            print("--- peering %s in %s: %s" % (p.get("Name"), part, p.get("State")))
EOF
for s in $(awk 'NR>2 && $1 ~ /-server-1$/ {print $1}' <<<"$st" | sort -u); do
  echo "=== $s: catalog health and config entries"
  $P exec "$F" "$s" -- python3 -c "$PY" </dev/null 2>&1
done
