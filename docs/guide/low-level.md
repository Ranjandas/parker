---
title: Low-level nodes
description: Add networks and nodes that aren't Consul, run any process in them, and control start order, readiness and restarts.
---

Under the `consul:` tree, Parker works with two simple things: networks
(bridges) and nodes (network namespaces running processes). You can
write those directly at the top level of a lab file, for anything that
isn't a Consul agent: a load generator, a proxy like toxiproxy, a
database, a custom binary.

```yaml
parker: v1alpha1
name: raw
networks:
  lan: { subnet: 10.50.0.0/24 }
nodes:
  box:
    networks: { lan: { ip: 10.50.0.10 } }
    processes:
      sleeper: { run: sleep infinity, restart: always }
```

`parker init --pattern raw` writes this file.

## Networks

| Key | Default | Meaning |
|---|---|---|
| `subnet` | | The bridge's subnet, such as `10.9.0.0/24`. |
| `gateway` | the subnet's `.1` | The host side of the bridge. |
| `auto` | `false` | Let `parker up` move the network, and every address in it, to a free subnet of the same size when this one is in use. Parker sets it on the networks it generates without a `subnet:` from you, so it shows up in `parker expand`. See [Several labs at once](lifecycle.md#several-labs-at-once). |

Every datacenter's LAN is a network too, named `<dc>-lan`, and a
low-level node can join it. This puts a node next to the Consul agents,
outside Consul:

```yaml
consul:
  datacenters:
    dc1: { servers: 1, subnet: 10.9.0.0/24 }
nodes:
  toolbox:
    networks: { dc1-lan: { ip: 10.9.0.200 } }
    processes:
      idle: { run: sleep infinity }
```

Pick addresses outside the ones Parker gives Consul nodes (`.10` and up
for servers, `.100` and up for others). For a node on a datacenter's
LAN without picking an address, a Consul node with
[`agent: none`](nodes-and-services.md#nodes-without-consul) is simpler.

## Nodes

| Key | Meaning |
|---|---|
| `networks` | The networks the node joins, each with its `ip`. |
| `files` | Files to write into the node's directory before anything starts: path to content. |
| `processes` | The processes to run, keyed by name. |
| `labels` | Where an expanded node came from. Parker writes these; [selectors](selectors.md) match on them. |

```yaml
nodes:
  web:
    networks: { lan: { ip: 10.50.0.20 } }
    files:
      index.html: "<h1>hello from the lab</h1>"
    processes:
      http: { run: "python3 -m http.server 8000", ready_when: { port: 8000 } }
```

## Processes

| Key | Meaning |
|---|---|
| `run` | The command. A string is split on spaces (no shell quoting); a list is used as is. |
| `env` | Environment variables. |
| `depends_on` | Processes to wait for first. |
| `ready_when` | When the process counts as ready. |
| `restart` | `no` (default), `on-failure` or `always`. |
| `version` | Use this version of the command's binary instead of the one under `versions:`. |
| `uses` | Other pinned binaries the process runs, to fetch and put on `PATH`. |

A process runs in its node's directory, as the user the daemon runs as,
with its output in `<process>.log` there. Its environment has `PATH`,
`HOME` (the node's directory), `PARKER_LAB`, `PARKER_NODE` and what you
set in `env:`.

### Pinned binaries

When the command's first word is a binary from `versions:` (`consul`,
`envoy`, `consul-dataplane`), Parker runs that version, downloading it
first if needed:

```yaml
versions: { consul: 1.22.7, envoy: 1.35.12 }
nodes:
  watcher:
    networks: { dc1-lan: { ip: 10.1.0.200 } }
    processes:
      watch:
        run: [consul, watch, -http-addr=10.1.0.10:8500, -type=nodes, cat]
        version: 1.21.5                    # this process only
```

### Start order

`depends_on` names processes as `<node>/<process>`, or just `<process>`
on the same node. By default a process waits for them to start;
`condition: ready` waits for their readiness check:

```yaml
processes:
  app:
    run: ./app
    depends_on:
      db/postgres: { condition: ready }
      sidecar: {}
```

### Readiness

`ready_when` says when the process is ready. `parker up` returns once
every process with a check is ready, and stops with the log when one
times out.

| Check | Ready when |
|---|---|
| `port: 8080` | The TCP port accepts connections. |
| `http: { url, status }` | The URL answers with `status` (default 200), from inside the node. |
| `log: TEXT` | The process's log contains TEXT. |
| `consul: { ... }` | The local Consul agent reports a state: `leader: true`, `members: ">=4"`, `wan_members: ">=6"`, `service_healthy: web`, `reaches: dc2`. |
| `timeout: 30s` | How long to wait. |

```yaml
ready_when:
  http: { url: "http://127.0.0.1:8080/health", status: 200 }
  timeout: 30s
```

The `consul:` check also takes `addr`, `token`, `ca_file` (for HTTPS),
and `partition` and `namespace` for `service_healthy` (Enterprise).

### Restarts

`restart: on-failure` restarts a process that exits with an error;
`always` restarts it whatever the exit, with a backoff from 1s to 30s.
Restarts apply once the lab is up, while the
[daemon](concepts.md#the-daemon) supervises it. `parker status` counts
them.

## Runtime paths

Commands and files can use paths Parker fills in when the lab starts:

| Variable | Is |
|---|---|
| `${node_dir}` | The node's directory under the lab's run directory. |
| `${run_dir}` | The lab's run directory. |
| `${parker}` | The `parker` binary itself. |

```yaml
processes:
  agent:
    run: [consul, agent, "-config-file=${node_dir}/consul.json"]
```

## Seeing what Parker generates

The `consul:` tree turns into exactly this form. `parker expand` prints
it for a whole lab, and `parker describe LAB NODE` for one node. Both
are good starting points for a low-level node of your own.
