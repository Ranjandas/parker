# Troubleshooting a lab

When something in a lab doesn't work, start here: how to reach Consul
with the right token, which token each part of the lab uses, and where
the logs and the diagnostics are.

## Talk to Consul

`parker exec FILE NODE` opens a shell inside a node with
`CONSUL_HTTP_ADDR`, `CONSUL_HTTP_TOKEN` (the lab's management token) and
`CONSUL_CACERT` already set, so `consul` works as is:

```sh
parker exec lab.yaml dc1/server-1 -- consul members
parker exec lab.yaml dc1/server-1 -- consul acl token read -self
```

To use the `consul` CLI on the host instead, `--env` prints the same
settings as export lines, pointing at the node's own address:

```sh
eval "$(parker exec lab.yaml dc1/server-1 --env)"
consul catalog services
```

This needs the host to reach the node's address, which it does under a
root daemon but not under a rootless one (use `parker exec`, or
`parker port-forward` / `parker proxy`). `parker up` prints the
command when ACLs are on. The output holds the management token: it's a
lab token, but think twice before pasting it into an issue.

The web UI's Consul *Open* link has a *Copy token* button too.

## Which token does what

With `acl: true` every part of the lab gets its own token, as Consul's
docs set them up:

| Who | Token |
|---|---|
| Client agent | node identity, in its partition |
| Service, its sidecar Envoy, its consul-dataplane | service identity, in its partition and namespace |
| Mesh gateway | service identity + `parker-mesh-gateway-*` policy (`mesh = write`, `peering = read`, reads) |
| Terminating gateway | service identity + write on the services it fronts |
| API gateway | service identity + `mesh = read` |
| Ingress gateway | service identity |
| DNS on transparent-proxy nodes | `parker-dns` policy |
| Servers | the `parker-agent` token |
| Setup jobs (acl-setup, tenancy, config entries, peering, dataplane-health), `parker exec` | management |

[ACL tokens](acl.md) lists every token's policies and their exact rules.

With namespaces (Consul Enterprise), services and gateways also get
`parker-read-everywhere`: service and node read in every namespace of
their partition, which cross-namespace upstreams need. A service or
gateway in a namespace other than `default` has its token in the
partition's default namespace instead, with a `parker-service-<ns>-<svc>`
(or `parker-<kind>-gateway-<ns>-<svc>`) policy that grants the service
identity's rules inside a `namespace "<ns>"` block: Enterprise only
links a token to its own namespace's policies, and only default-namespace
policies can grant partition-wide rules.

The `tenancy` process on each datacenter's `server-1` creates the
policies and tokens before any client starts; its log lists each one.
`parker describe FILE NODE` prints a node's rendered configs and process
environments, and ends with a note naming every token in them:

```
# ACL tokens above:
#   2f1c…  node identity web (dc1)
#   9a07…  service identity web; namespace app; policies parker-read-everywhere
```

So when a sidecar can't see its upstreams, read its token's policies
(`consul acl token read -id <AccessorID>`, from `consul acl token list`)
rather than the management token's, which sees everything.

## Logs

- `parker logs FILE NODE [PROCESS] -f` follows a process's log;
  `--level debug` changes a running Consul agent's or Envoy's log level
  (see `parker logs --help`).
- The files are under the run dir: `/var/lib/parker/run/<lab>/nodes/<node>/`
  (root) or `~/.local/state/parker/run/<lab>/nodes/<node>/` (rootless).
  `parker down` keeps them; `parker down --purge` removes them.

## The mesh

From inside a node, Consul's own tools work against its sidecar:

```sh
parker exec lab.yaml dc1/web -- consul troubleshoot upstreams -envoy-admin-endpoint 127.0.0.1:19000
parker exec lab.yaml dc1/web -- consul troubleshoot proxy -envoy-admin-endpoint 127.0.0.1:19000 -upstream-envoy-id api
```

The first sidecar on a node has its Envoy admin on `127.0.0.1:19000`, the
next on 19001, and so on (`parker describe` shows each `-admin-bind`).

`hack/lab-diag.sh FILE` prints a running lab's whole mesh at once: every
node's processes, each Envoy's recent log and cluster health, and from
each datacenter's first server the health checks, connect endpoints,
config entries and peerings of every partition and namespace. It only
reads, and it's what to attach to a bug report.
