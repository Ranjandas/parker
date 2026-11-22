---
title: Service mesh
description: Put services in Consul's service mesh with sidecar or transparent proxies, declare upstreams, and control traffic with intentions.
---

`connect:` on a service puts it in Consul's service mesh. Parker turns
Connect on, registers the sidecar proxy, and runs Envoy for it. Pin an
Envoy version next to Consul's:

```yaml
parker: v1alpha1
versions: { consul: 1.22.7, envoy: 1.35.12 }
consul:
  datacenters:
    dc1:
      servers: 3
      service_template: { run: fake-service, connect: sidecar }
      nodes:
        web:
          service:
            port: 8080
            upstreams: { api: 9091 }       # web reaches api on 127.0.0.1:9091
        api:
          service: { port: 9090 }
```

```console
$ parker up mesh.yaml
$ parker call mesh dc1/web
web  dc1-web · dc1  200  2.887612ms
└─ api  dc1-api · dc1  200  5.48µs
```

[`parker call`](fake-service.md#seeing-the-chain) shows the request went
from web through the mesh to api. Each sidecar runs as the process
`<service>-sidecar` on the service's node, and Consul registers it as
`<service>-sidecar-proxy`.

> **Tip:** `parker validate` warns when the pinned Envoy isn't one the
> pinned Consul supports. Consul's docs list the supported pairs.

## Connect modes

| `connect:` | What it does |
|---|---|
| `sidecar` | Registers a sidecar proxy and runs Envoy for it. The service reaches others through the upstreams it declares. |
| `tproxy` | A transparent proxy: the node's traffic goes through Envoy without the app knowing. One per node. |
| `none` | The default: not in the mesh. |

## Upstreams

An upstream is a service this one calls through its sidecar. The short
form is the local port the sidecar listens on, at `127.0.0.1`:

```yaml
service:
  port: 8080
  connect: sidecar
  upstreams: { api: 9091, db: 9092 }
```

The long form also says where the destination is, for a service in
another datacenter, behind a [peering](multiple-datacenters.md#cluster-peering),
or in another [partition or namespace](enterprise.md):

```yaml
upstreams:
  api:     { port: 9091, dc: dc2 }                 # a federated datacenter
  billing: { port: 9092, peer: dc1-dc2 }           # imported over a peering
  ledger:  { port: 9093, partition: team-a, namespace: payments }
```

| Key | Meaning |
|---|---|
| `port` | The port the sidecar binds on `127.0.0.1`. |
| `dc` | The destination's datacenter, when it isn't the caller's. |
| `peer` | The peering the destination is imported from. |
| `partition`, `namespace` | The destination's admin partition and namespace (Enterprise). |

A [fake service](fake-service.md) calls its upstreams on every request,
so a chain of fakes shows the whole path with no code.

## Transparent proxy

With `connect: tproxy`, Parker redirects the node's outbound TCP through
Envoy and its DNS to the local Consul agent. The app calls other services
by their Consul name, with no upstreams declared:

```yaml
dc1:
  servers: 3
  service_template: { run: fake-service }
  nodes:
    web:
      service: { port: 8080, connect: tproxy }
    api:
      service: { port: 9090, connect: sidecar }
```

```console
$ parker exec tproxy dc1/web -- curl -s http://api.virtual.consul:9090/
```

Inbound traffic goes through Envoy too, except Consul's own ports, so a
caller outside the mesh is refused. That includes
[`parker port-forward`](getting-in.md#port-forwarding) to the app's port.
Transparent proxying works in [rootless](rootless.md) labs as well.

## Intentions

Consul's intentions decide which services may call which. Without
[ACLs](acl-and-tls.md) everything is allowed by default. With ACLs on,
everything is denied until an intention allows it.

Write them as `service-intentions` [config entries](config-entries.md),
so the lab starts with them in place:

```yaml
dc1:
  config_entries:
    - Kind: service-intentions
      Name: api
      Sources: [{ Name: web, Action: allow }]
```

Or change them while the lab runs, and watch the effect:

```console
$ parker exec mesh dc1/server-1 -- consul intention create -deny web api
$ parker call mesh dc1/web
web  dc1-web · dc1  500  1.748478ms  upstream http://127.0.0.1:9091/ failed
└─ http://127.0.0.1:9091/  failed  1.59998ms  Get "http://127.0.0.1:9091/": EOF
```

## Traffic management

Routing, splitting, failover and protocols are Consul config entries
(`service-defaults`, `service-router`, `service-splitter`,
`service-resolver`, `proxy-defaults`). Add them under
[`config_entries`](config-entries.md), pasted from Consul's docs as they
are. For example, to send a tenth of api's traffic to a canary:

```yaml
nodes:
  api:
    count: 2
    service: { port: 9090 }
  api-canary:
    services:
      api:                             # another instance of api, as v2
        port: 9090
        fake: { version: v2 }
        register: { meta: { version: v2 } }
config_entries:
  - { Kind: service-defaults, Name: api, Protocol: http }
  - Kind: service-resolver
    Name: api
    Subsets:
      v1: { Filter: 'Service.Meta.version != v2' }
      v2: { Filter: 'Service.Meta.version == v2' }
  - Kind: service-splitter
    Name: api
    Splits:
      - { Weight: 90, ServiceSubset: v1 }
      - { Weight: 10, ServiceSubset: v2 }
```

```console
$ parker call lab.yaml dc1/web --every 100ms --count 30
...
12:37:10  200  2.320282ms web@dc1 → api@dc1
12:37:10  200  6.592916ms web@dc1 → api@dc1/v2
--- 30 requests, 0 failed
    api: dc1 87% · dc1 v2 13%
```

`--every` keeps calling, shows which version answered each request, and
ends with the split.

## Debugging the mesh

- `parker status LAB` shows each sidecar's state. A sidecar that never
  gets ready usually can't reach its local agent or has no certificate.
- `parker logs LAB dc1-web/web-sidecar` is the sidecar's Envoy log;
  `--level debug` (or `--logger upstream=trace`) raises it without a
  restart. See [log levels](log-levels.md).
- Envoy's admin API is on the node, at `127.0.0.1:19000` for its first
  Envoy (19001 for the next): `parker exec LAB dc1/web -- curl -s
  localhost:19000/clusters`.

[Troubleshooting](troubleshooting.md#the-mesh) has more.
