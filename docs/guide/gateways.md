---
title: Gateways
description: Run mesh, ingress, terminating and API gateways, write their config entries inline, and register services that live outside Consul.
---

Gateways go under a datacenter's `gateways:`, keyed by name. Each
gateway is a node of its own, running a Consul client agent and the
gateway's Envoy. Like sidecars, gateways need `versions.envoy`.

```yaml
dc1:
  servers: 3
  gateways:
    mesh: true                                  # a mesh gateway, node dc1-mesh
    ingress:
      entry:
        Listeners:
          - { Port: 8080, Protocol: http, Services: [{ Name: api, Hosts: ["*"] }] }
```

## Gateway settings

| Key | Default | Meaning |
|---|---|---|
| `kind` | the gateway's name | `mesh`, `ingress`, `terminating` or `api`. |
| `entry` | | The gateway's config entry, in Consul's casing, without `Kind` and `Name`. |
| `count` | `1` | Run N gateway nodes, named `<name>-1` ... `<name>-N`. |
| `wan` | on when federated or peered | Mesh gateways only: also join the `wan` network. |
| `namespace` | | Terminating, ingress and API gateways: the namespace to register in (Enterprise). |

The name is the gateway's node name, and its kind when `kind:` is left
out. So `ingress: { entry: ... }` is an ingress gateway, and
`edge: { kind: api, entry: ... }` is an API gateway on the node
`dc1-edge`. Consul registers a gateway as `<kind>-gateway` when its name
is its kind (`ingress-gateway`), and under its own name otherwise
(`edge`).

Parker adds `Kind` and `Name` to `entry:` and applies it after the
datacenter's [config entries](config-entries.md), so entries the gateway
needs (such as a service's protocol) are in place first.

## Mesh gateways

A mesh gateway carries mesh traffic between datacenters, peers and
partitions. `mesh: true` is the short form:

```yaml
gateways:
  mesh: true
```

For more than one, use the long form:

```yaml
gateways:
  mesh: { kind: mesh, count: 2 }
```

Mesh gateways listen on port 8443. Federation through mesh gateways and
peering both use them: see [multiple datacenters](multiple-datacenters.md).
Services use them when `proxy-defaults` (or the upstream) sets the mesh
gateway mode:

```yaml
config_entries:
  - Kind: proxy-defaults
    Name: global
    MeshGateway: { Mode: local }
```

## Ingress gateways

An ingress gateway lets callers outside the mesh reach services inside
it. Its listeners need an HTTP protocol on the services they route to:

```yaml
dc1:
  servers: 1
  service_template: { run: fake-service }
  gateways:
    ingress:
      entry:
        Listeners:
          - { Port: 8080, Protocol: http, Services: [{ Name: api, Hosts: ["*"] }] }
  nodes:
    api:
      service: { port: 9090, connect: sidecar }
  config_entries:
    - { Kind: service-defaults, Name: api, Protocol: http }
```

```console
$ parker exec gateways dc1/ingress -- sh -c 'curl -s http://$PARKER_IP:8080/'
```

`parker exec` sets `PARKER_IP` to the node's LAN address, which is
where the gateway listens.

## API gateways

An API gateway takes its listeners from `entry:` and its routes from
`http-route` or `tcp-route` config entries that name it as a parent:

```yaml
gateways:
  edge:
    kind: api
    entry:
      Listeners: [{ Name: http, Port: 8081, Protocol: http }]
config_entries:
  - { Kind: service-defaults, Name: api, Protocol: http }
  - Kind: http-route
    Name: edge-to-api
    Parents: [{ Kind: api-gateway, Name: edge, SectionName: http }]
    Rules: [{ Services: [{ Name: api }] }]
```

## Terminating gateways

A terminating gateway lets mesh services reach services that aren't in
the mesh. List those under `Services`:

```yaml
dc1:
  servers: 1
  service_template: { run: fake-service }
  gateways:
    terminating:
      entry:
        Services: [{ Name: legacy }]
  nodes:
    legacy:
      service: { port: 7070 }                  # registered, but not in the mesh
    web:
      service:
        port: 8080
        connect: sidecar
        upstreams: { legacy: 9092 }
```

web reaches legacy on `127.0.0.1:9092`, through its sidecar and the
terminating gateway. With [ACLs](acl-and-tls.md) on, Parker gives the
gateway the rights to the services it fronts.

## External services

An external service runs outside Consul altogether: no agent, no
sidecar. It's registered in the catalog on an external node, the way
consul-esm or a hand-made registration would, and mesh services reach it
through a terminating gateway. Declare it under `consul.external`:

```yaml
consul:
  defaults:
    service_template: { run: fake-service }
  external:
    api:
      service: { port: 9000 }
      in: [dc1, dc2]                          # register it in both datacenters
  datacenters:
    dc1:
      servers: 1
      gateways:
        terminating: { entry: { Services: [{ Name: api }] } }
      nodes:
        web:
          service: { port: 8080, connect: sidecar, upstreams: { api: 9091 } }
    dc2:
      servers: 1
```

| Key | Meaning |
|---|---|
| `service` | What runs: `port`, `run`, `fake`, `env`, `check`, `namespace`. It takes `service_template` from `consul.defaults`, but never joins the mesh. |
| `in` | Where it's registered: datacenters (`dc1`) or admin partitions (`dc1/team-a`). |

The service runs once, on a node named after it that joins the LAN of
every datacenter under `in:`. Each of them registers it with node meta
`external-node: "true"`, and Parker checks its health from the
datacenter's first server, as consul-esm would. Stop it with a
[fault](faults.md) and its health turns critical everywhere.

For a full failover demo (terminating gateways in two peered
datacenters), see
[examples/terminating-failover.yaml](https://github.com/Ranjandas/parker/blob/main/examples/terminating-failover.yaml).

## Gateways in a namespace

> **Enterprise:** `namespace:` on a terminating, ingress or API gateway
> registers it, and its config entry, in that namespace. Mesh gateways
> always sit in their partition's default namespace. Gateways in an
> admin partition go under that partition's `gateways:`. See
> [Enterprise](enterprise.md).
