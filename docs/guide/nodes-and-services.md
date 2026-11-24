---
title: Nodes and services
description: Add client nodes, run services on them, register them in Consul with health checks, and share settings between services.
---

Nodes besides the servers go under a datacenter's `nodes:`, keyed by
name. By default a node runs a Consul client agent, plus whatever
services and processes you give it.

```yaml
dc1:
  servers: 3
  nodes:
    web:
      count: 2
      service: { port: 8080, run: fake-service }
    client: {}                  # a bare client agent
```

This adds `dc1-web-1` and `dc1-web-2`, each running a client agent and
the `web` service, and `dc1-client`, a client agent with nothing else.

## Node settings

| Key | Default | Meaning |
|---|---|---|
| `count` | `1` | Make N copies, named `<name>-1` ... `<name>-N`. |
| `agent` | `client` | `client` runs a Consul client agent; `dataplane` runs none and uses consul-dataplane for sidecars (see [dataplanes](dataplanes.md)); `none` runs no Consul at all. |
| `service`, `services` | | The services the node runs (below). |
| `meta` | | Consul node metadata. |
| `config` | | Merged into this node's agent configuration, as in [agent configuration](datacenters.md#agent-configuration). |
| `processes` | | Extra processes to run on the node (below). |
| `from` | | Start from a [template](reuse.md#templates). |

```yaml
nodes:
  web:
    count: 3
    meta: { zone: a, rack: r1 }
    config: { enable_local_script_checks: true }
    service: { port: 8080, run: fake-service }
```

## Services

`service:` is one service named after the node. `services:` is any number
of them, keyed by service name:

```yaml
nodes:
  web:
    service: { port: 8080, run: fake-service }       # the service "web"
  shop:
    services:
      cart: { port: 8080, run: fake-service }
      checkout: { port: 8081, run: fake-service }
```

Parker registers each service with the node's agent and starts what it
runs. `parker up` waits until the service's port answers and Consul
reports it healthy.

| Key | Meaning |
|---|---|
| `port` | The port the service listens on, and registers with. |
| `run` | What to run: `fake-service` for Parker's built-in [fake service](fake-service.md), or a command. Leave it out to register a service that nothing serves. |
| `env` | Environment variables for the process. |
| `check` | The health check: `http` (a GET on `/`), `tcp`, `grpc` or `none`. Default: `http` for a command, the fake's own `/health` check for the fake service. |
| `register` | Merged into the Consul service registration as is: tags, meta, extra checks, weights. |
| `fake` | Settings for `run: fake-service`. See [the fake service](fake-service.md). |
| `connect` | `sidecar` or `tproxy` puts the service in the mesh. See [service mesh](service-mesh.md). |
| `upstreams` | The services this one calls through its sidecar. See [service mesh](service-mesh.md#upstreams). |
| `namespace` | The Consul namespace to register in (Enterprise). See [Enterprise](enterprise.md#namespaces). |

### Running your own program

`run:` can be any program on the host. It runs inside the node's network
namespace, so it listens on the node's address. A string is split on
spaces, with no shell quoting; for pipes, quotes or variables, give a list
and run a shell yourself:

```yaml
nodes:
  files:
    service:
      port: 8000
      run: "python3 -m http.server 8000"
      check: tcp
  cache:
    service:
      port: 6379
      run: "redis-server --port 6379 --protected-mode no"
      check: tcp
      register: { tags: [primary], meta: { version: "7" } }
  worker:
    service:
      port: 9000
      run: [sh, -c, "exec ./worker --listen :9000 2>&1 | tee -a worker.out"]
```

A process starts in its node's directory under the lab's run directory,
and its output goes to `<process>.log` there (read it with
[`parker logs`](getting-in.md#logs)). It doesn't inherit your shell's
environment: it gets `PATH` (with the binaries the lab pins, such as
`consul`, first), `HOME` set to the node's directory, `TMPDIR` set to
its own `tmp/<process>` directory there, `PARKER_LAB`, `PARKER_NODE`, and
what you set in `env:`.

### Registering in Consul

`register:` uses Consul's own [service definition](https://developer.hashicorp.com/consul/docs/services/configuration/services-configuration-reference)
field names, and is merged over what Parker generates:

```yaml
service:
  port: 8080
  run: fake-service
  register:
    tags: [v2, canary]
    meta: { team: payments }
    weights: { passing: 10, warning: 1 }
```

## Service template

`service_template:` on a datacenter (or under `consul.defaults`) is merged
into every service, field by field. It keeps labs with many services
short. It is a Parker shortcut, unrelated to Consul's `service-defaults`
config entry (set that under [`config_entries`](config-entries.md)):

```yaml
dc1:
  servers: 1
  service_template: { run: fake-service, connect: sidecar }
  nodes:
    web: { service: { port: 8080, upstreams: { api: 9091 } } }
    api: { service: { port: 9090 } }
    db:  { service: { port: 5432 } }
```

## Extra processes

`processes:` runs more programs on a node, next to its agent and
services. Each takes a command, and optionally an environment, a restart
policy, what to wait for, and a readiness check:

```yaml
nodes:
  web:
    service: { port: 8080, run: fake-service }
    processes:
      load:
        run: [sh, -c, "while sleep 1; do curl -s localhost:8080 >/dev/null; done"]
        restart: always
```

The keys are the same as for [low-level nodes](low-level.md#processes).

## Nodes without Consul

`agent: none` makes a node with no Consul agent: just a network namespace
on the datacenter's LAN, running its processes. It's useful for a client
or a tool that should see the lab from outside Consul.

```yaml
nodes:
  toolbox:
    agent: none
    processes:
      idle: { run: "sleep infinity" }
```

For a service that runs outside Consul but is registered in its catalog,
reached through a terminating gateway, use
[external services](gateways.md#external-services).
