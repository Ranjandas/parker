---
title: Troubleshooting
description: Reach Consul with the right token, find which token each part of a lab uses, read logs, debug the mesh, and get parker machine going.
---

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

[ACL tokens](acl-tokens.md) lists every token's policies and their exact rules.

With namespaces (Consul Enterprise), services and gateways also get
`parker-read-everywhere`: service and node read in every namespace of
their partition, which cross-namespace upstreams need. A token lives in
its service's namespace, so look for it there
(`consul acl token list -partition part-a -namespace app`). Outside
`default`, `parker-read-everywhere` comes from the namespace's ACL
defaults rather than the token's own policies. An API gateway, or a
terminating gateway fronting a service in another namespace, has a
[scoped token](acl-tokens.md#scoped-tokens) in the partition's default
namespace instead.

The `tenancy` process on each datacenter's `server-1` creates the
policies and tokens before any client starts; its log lists each one.
`parker describe FILE NODE` prints a node's rendered configs and process
environments, and ends with a note naming every token in them:

```
# ACL tokens above:
#   2f1c…  node identity web (dc1)
#   9a07…  service identity web; partition part-a; namespace app
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

## parker machine doesn't start

If `parker machine start` keeps printing "still booting", the VM is up
but its daemon hasn't reported in yet. It gives up after 10 minutes and
prints the end of its log. Look in this order:

1. **Did the VM boot?** The console log ends at a
   `parker-machine login:` prompt once it has:

   ```console
   $ tail -5 ~/.parker/machine/default/console.log
   ```

2. **Where is the daemon?** `parker machine ssh` needs a machine that's
   ready, so log in by hand. macOS's DHCP leases have the VM's address,
   under the name `parker-machine`:

   ```console
   $ grep -A1 name=parker-machine /var/db/dhcpd_leases
   	name=parker-machine
   	ip_address=192.168.64.2
   $ ssh -i ~/.parker/machine/default/id_ed25519 parker@192.168.64.2 \
       'cloud-init status --long; sudo journalctl -u parker -u parker-machine-ready -b --no-pager | tail -40'
   ```

   `parker.service` stuck in "Starting" means it's still installing
   Parker. That needs `github.com` and `ranjandas.github.io` to be
   reachable from the VM.

3. **What did the VM process say?** `~/.parker/machine/default/machine.log`
   has every start and stop, and the error when the VM couldn't start at
   all. "The process doesn't have the com.apple.security.virtualization
   entitlement" means a `parker` that wasn't signed for VMs, such as one
   built with plain `go build`. Use a release, or build with
   `make machine` (see [labs on macOS](macos.md#requirements)).

## parker machine stops responding

If labs in the VM stop answering, `parker exec` or `parker status LAB`
fails with `input/output error`, and `parker machine ssh` gets
"Connection refused", the VM's disk has failed under it (seen with
Parker 0.12.0; upgrade `parker` on your Mac first).

To recover, try restarting the machine first. The VM checks its disk as
it boots, and labs come back with `parker up`:

```console
$ parker machine stop
$ parker machine start
$ parker up examples/minimal.yaml
```

If `start` stays at "still booting", the disk is beyond repair. Create
the machine again. The image is cached, so this takes about a minute:

```console
$ parker machine rm --force
$ parker machine init
$ parker machine start
```

If the errors come back on a current release, please
[open an issue](https://github.com/Ranjandas/parker/issues) with the
lines from `console.log` and your macOS version.
