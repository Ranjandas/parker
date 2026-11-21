---
title: Multiple datacenters
description: Run several datacenters, WAN-federate them directly or through mesh gateways, and connect them with cluster peering.
---

Every datacenter under `consul.datacenters` is its own Consul cluster on
its own LAN. On their own they don't know about each other. Two ways
connect them:

| | WAN federation | Cluster peering |
|---|---|---|
| Set with | `consul.federation` | `consul.peering` |
| What it joins | The servers of every datacenter, into one federation with one primary | Two clusters (or admin partitions), as peers |
| Services | Any service, by datacenter (`dc: dc2`) | Only what the other side exports (`peer: NAME`) |
| ACLs | Replicated from the primary | Each side has its own |

## WAN federation

`federation: { mode: wan }` joins every datacenter's servers over a
generated `wan` network, with Serf WAN gossip:

```yaml
consul:
  federation: { mode: wan }
  datacenters:
    dc1:
      primary: true
      servers: 3
      nodes:
        client: {}
    dc2:
      servers: 1
      nodes:
        api:
          service: { port: 9090, run: fake-service }
```

```console
$ parker up federation.yaml
$ parker exec federation dc1/server-1 -- consul members -wan
$ parker exec federation dc1/client -- consul catalog services -datacenter dc2
```

- The primary is the datacenter with `primary: true`, or the first by
  name.
- Servers keep their LAN address and also join `wan`, `10.255.0.0/24`
  (dc1's servers at `.10` and up, dc2's at `.30` and up, and so on).
  Client agents stay on their own LAN. To move the `wan` network, declare
  it: `networks: { wan: { subnet: 10.200.0.0/24 } }`.
- `parker up` waits until every server sees every other server over the
  WAN.
- With [ACLs](acl-and-tls.md), secondaries replicate tokens and policies
  from the primary. ACL and TLS settings must agree across federated
  datacenters, so set them in `consul.defaults`.

A mesh service calls a service in another datacenter with `dc:` on its
upstream:

```yaml
upstreams: { api: { port: 9091, dc: dc2 } }
```

## Federation through mesh gateways

With `mode: mesh-gateways`, servers keep Serf on their LAN, which other
datacenters can't reach, and talk to other datacenters only through the
mesh gateways. It needs `tls: true` and a mesh gateway in each
datacenter:

```yaml
parker: v1alpha1
versions: { consul: 1.22.7, envoy: 1.35.12 }
consul:
  defaults:
    tls: true
    service_defaults: { run: fake-service, connect: sidecar }
  federation: { mode: mesh-gateways }
  datacenters:
    dc1:
      servers: 1
      gateways: { mesh: true }
      nodes:
        web:
          service:
            port: 8080
            upstreams: { api: { port: 9091, dc: dc2 } }
      config_entries:
        - { Kind: proxy-defaults, Name: global, MeshGateway: { Mode: local } }
    dc2:
      servers: 1
      gateways: { mesh: true }
      nodes:
        api:
          service: { port: 9090 }
```

Secondaries start once the primary's gateway is ready, and `parker up`
waits until the primary reaches every secondary. Only the mesh gateways
join the `wan` network.

## Cluster peering

`consul.peering` connects two datacenters as peers, keyed by the
peering's name, which is the peer's name on both sides. One side, the
acceptor, generates a peering token; the other, the dialer, uses it.
Peering needs Consul 1.14 or later and a mesh gateway on each side:

```yaml
parker: v1alpha1
versions: { consul: 1.22.7, envoy: 1.35.12 }
consul:
  defaults:
    service_defaults: { run: fake-service, connect: sidecar }
  peering:
    dc1-dc2:
      acceptor: dc2
      dialer: dc1
      exports: { dc2: [api] }          # dc2 exports api to dc1
  datacenters:
    dc1:
      servers: 1
      gateways: { mesh: true }
      nodes:
        web:
          service:
            port: 8080
            upstreams: { api: { port: 9091, peer: dc1-dc2 } }
      config_entries:
        - { Kind: proxy-defaults, Name: global, MeshGateway: { Mode: local } }
    dc2:
      servers: 1
      gateways: { mesh: true }
      nodes:
        api:
          service: { port: 9090 }
```

```console
$ parker up peering.yaml
$ parker exec peering dc1/server-1 -- consul peering list
$ parker call peering dc1/web
```

`parker up` waits until the peering is active.

| Key | Meaning |
|---|---|
| `acceptor` | The side that generates the token: a datacenter, or `dc/partition` (Enterprise). |
| `dialer` | The side that establishes the peering with it. |
| `exports` | Per side, the services it exports to the other: `{ dc2: [api] }`, or `namespace/service` for a namespaced one. Parker writes the `exported-services` entry, merged into one you wrote yourself. |
| `via_mesh_gateways` | Send the peering's control plane through the mesh gateways too. Without it, the servers join the `wan` network and reach each other directly. |

Services reach an imported service with `peer:` on the upstream. With
[ACLs](acl-and-tls.md) on, the exporting side also needs an intention
that allows the peer's caller:

```yaml
config_entries:
  - Kind: service-intentions
    Name: api
    Sources: [{ Name: web, Peer: dc1-dc2, Action: allow }]
```

### Peering admin partitions

> **Enterprise:** a side can be an admin partition, `dc2/team-b`. The
> token and the peering are made in that partition, which needs its own
> mesh gateway, and `exports: { dc2/team-b: [payments/billing] }`
> exports from it. With `via_mesh_gateways`, the control plane goes
> through each datacenter's default-partition gateway (Consul allows it
> only there), so the default partitions need a mesh gateway too.
> Two partitions of the same datacenter can't peer: export to the other
> partition with an `exported-services` entry instead. See
> [Enterprise](enterprise.md).

## Failover across datacenters

A `service-resolver` with `Failover` sends traffic to another datacenter
or peer when the local instances are unhealthy. For example, when dc1
runs its own api and also imports dc2's over the peering above:

```yaml
config_entries:
  - Kind: service-resolver
    Name: api
    ConnectTimeout: 2s
    Failover:
      "*": { Targets: [{ Peer: dc1-dc2 }] }
```

Then [stop](faults.md) the local instances and watch the calls move:

```console
$ parker call lab.yaml dc1/web --every 1s     # in one terminal
$ parker fault lab.yaml stop dc1/api          # in another
```

## Breaking the link

A [network partition](faults.md#partitions) between datacenters is one
line:

```console
$ parker fault lab.yaml partition dc1 dc2
$ parker fault lab.yaml heal
```
