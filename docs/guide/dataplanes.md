---
title: Dataplanes
description: Run services without a Consul client agent, with consul-dataplane sidecars, as Consul on Kubernetes does.
---

On Kubernetes, Consul runs no client agents: services are registered in
the catalog directly, and each sidecar is `consul-dataplane`, which gets
its configuration from the servers over gRPC. `agent: dataplane` gives a
node the same setup, so you can reproduce Kubernetes behaviour without
Kubernetes:

```yaml
parker: v1alpha1
versions: { consul: 1.22.7, envoy: 1.35.12, consul-dataplane: 1.9.12 }
consul:
  datacenters:
    dc1:
      servers: 3
      service_template: { run: fake-service, connect: sidecar }
      nodes:
        web:                                  # an agent node
          service: { port: 8080, upstreams: { api: 9091 } }
        api:
          agent: dataplane                    # no agent
          service: { port: 9090, upstreams: { web: 9092 } }
```

A dataplane node needs `versions.consul-dataplane` and `versions.envoy`.
Agent and dataplane nodes mix freely in one datacenter, and reach each
other through the mesh.

## What Parker does

- A `dataplane-health` process on the datacenter's first server
  registers the node's services and sidecars in the catalog, and probes
  them every second, as the consul-k8s endpoints controller does. Stop
  the service and its check turns critical.
- Each sidecar is `consul-dataplane`, which finds the servers over gRPC
  (port 8502, or 8503 with [TLS](acl-and-tls.md#tls)) and starts Envoy.
- With [ACLs](acl-and-tls.md), each dataplane logs in with its service's
  identity token.

```console
$ parker exec dataplane dc1/server-1 -- consul catalog nodes -service api
$ parker logs dataplane dc1-api/api-sidecar -f
```

## Limits

- `connect: tproxy` isn't supported on dataplane nodes yet.
- consul-dataplane's own log level is fixed when it starts. Its Envoy's
  [log level](log-levels.md) can change.
