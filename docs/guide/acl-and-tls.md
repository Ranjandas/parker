---
title: ACLs and TLS
description: Turn on Consul ACLs, gossip encryption and TLS with one line each, and use the tokens and certificates Parker generates.
---

Securing a Consul cluster by hand takes a CA, certificates, a gossip key,
a bootstrap token and a token per agent and service. In a lab it's one
line each:

```yaml
consul:
  defaults: { acl: true, tls: true }
  datacenters:
    dc1:
      servers: 3
      nodes:
        web:
          service: { port: 8080, run: fake-service }
```

Set them under `consul.defaults` for every datacenter, or on one
datacenter. [Federated](multiple-datacenters.md) datacenters must agree.

## ACLs

`acl: true` turns on ACLs with default deny, plus gossip encryption.
Parker generates the management token and the gossip key, and gives
every part of the lab a token of its own, the way Consul's docs set
them up:

- each client agent, a node identity;
- each service, its sidecar and its consul-dataplane, a service identity;
- each gateway, a service identity plus the rules its kind needs;
- servers, an agent token;
- setup jobs (config entries, peering, partitions), the management token.

The `tenancy` process on each datacenter's first server creates them
before any client starts. [ACL tokens](acl-tokens.md) lists every token
and its exact rules.

### Using the management token

`parker exec` sets `CONSUL_HTTP_TOKEN` to the management token, so the
`consul` CLI works inside any node as is:

```console
$ parker exec acl dc1/server-1 -- consul acl token list
```

To use the `consul` CLI on the host, `--env` prints the settings to
`eval` (with a root daemon, where the host reaches the node's address):

```console
$ eval "$(parker exec acl dc1/server-1 --env)"
$ consul members
```

`parker up` prints that command when ACLs are on. The web UI's link to
the Consul UI has a *Copy token* button.

### Which token a node uses

`parker describe` ends with the tokens in a node's configs:

```console
$ parker describe tls dc1/web
...
# ACL tokens above:
#   4c03facb-8cfd-4562-ad99-ac23944a2fc9  service identity web
#   749c669f-ecb4-4364-b632-584d94eb739b  node identity web (dc1)
#   94fc1dd4-8a12-4232-9047-30374065a050  management token
```

### Intentions under ACLs

Default deny also applies to the mesh: services can't call each other
until a `service-intentions` entry allows it. See
[service mesh](service-mesh.md#intentions).

## TLS

`tls: true` turns on mutual TLS for RPC and makes agent APIs HTTPS only,
on port 8501:

- Parker creates a CA for the lab. It signs the servers' certificates
  (for `server.<dc>.consul`, their addresses and their
  [`parker proxy`](getting-in.md#proxy) names) and is also the mesh's
  Connect root.
- Client agents get their certificates through `auto_encrypt`.
- `parker exec` sets `CONSUL_HTTP_ADDR` and `CONSUL_CACERT`, so the CLI
  works unchanged:

```console
$ parker exec tls dc1/server-1 --env
# dc1-server-1 in lab tls; eval "$(parker exec tls dc1/server-1 --env)"
export CONSUL_HTTP_ADDR=https://10.1.0.10:8501
export CONSUL_CACERT=/var/lib/parker/run/tls/nodes/dc1-server-1/ca.pem
export CONSUL_HTTP_TOKEN=94fc1dd4-8a12-4232-9047-30374065a050
```

Each node has the CA as `ca.pem` in its directory. Federation through
mesh gateways needs `tls: true`.

> **Note:** these are only lab secrets, but the management token gives
> full control of the lab. Think twice before pasting it into an issue.
