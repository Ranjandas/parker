---
title: Datacenters and servers
description: Define datacenters, size their server clusters, mix versions, and pass settings through to the Consul agents.
---

Everything Consul lives under `consul.datacenters`, keyed by datacenter
name. A datacenter needs servers; everything else is optional.

```yaml
consul:
  datacenters:
    dc1:
      servers: 3
```

Each datacenter is its own Consul cluster on its own LAN. To connect
several, see [multiple datacenters](multiple-datacenters.md).

## Servers

`servers:` is the number of Consul servers. Parker names them `server-1`,
`server-2`, ..., gives them the first addresses of the LAN (`.10`, `.11`,
...), and `parker up` returns once they have elected a leader.

Use 1 for the fastest lab, 3 or 5 for anything about raft, leadership or
quorum.

### Per-server settings

The long form takes `count` and per-server `overrides`. An override can
pin a different Consul version, which makes a mixed-version cluster, or
add agent configuration to one server:

```yaml
dc1:
  servers:
    count: 3
    overrides:
      server-3:
        version: 1.21.5                       # this server runs an older Consul
        config: { autopilot: { min_quorum: 3 } }
```

To move servers to a new version while the lab runs, use a
[rolling upgrade](upgrades.md).

## Agent configuration

Three blocks pass settings through to the Consul agents' configuration
files. `config:` is merged into the agent's JSON config as is, using
Consul's own option names, and `log_level:` sets the agent's log level.

| Block | Applies to |
|---|---|
| `agents` | Servers and client agents |
| `server` | Servers only |
| `client` | Client agents only |

```yaml
dc1:
  servers: 3
  agents: { log_level: debug }
  server:
    config:
      raft_snapshot_threshold: 64
      performance: { raft_multiplier: 1 }
  client:
    config: { telemetry: { disable_hostname: true } }
```

A single node can add its own `config:` too (see
[nodes](nodes-and-services.md#node-settings)), and one server through its
override, as above. `parker describe LAB NODE` shows a node's final
`consul.json`.

> **Tip:** to change a running agent's log level without restarting
> anything, use [`parker logs --level`](log-levels.md).

## Defaults for every datacenter

`consul.defaults` holds settings every datacenter gets unless it sets its
own: `agents`, `server`, `client`, `service_template`, `acl` and `tls`.

```yaml
consul:
  defaults:
    acl: true
    tls: true
    agents: { log_level: info }
    service_template: { run: fake-service, connect: sidecar }
  datacenters:
    dc1: { servers: 3 }
    dc2: { servers: 1, acl: false }      # dc2 overrides one setting
```

`service_template` is merged into every service, which keeps files with
many services short. See [services](nodes-and-services.md#service-template).

## Other datacenter settings

| Key | Default | Meaning |
|---|---|---|
| `subnet` | `10.<n>.0.0/24` | The datacenter's LAN. Set it when the default clashes with a network you use, or when addresses matter to the repro. |
| `primary` | the first datacenter by name | Marks the primary datacenter, for federation and ACL replication. |
| `ui` | `true` | Serves the Consul web UI on the servers. |
| `acl`, `tls` | off | Turn on ACLs and TLS. See [ACLs and TLS](acl-and-tls.md). |
| `service_template` | | Settings merged into every service in this datacenter. |
| `nodes` | | Client nodes and services. See [nodes and services](nodes-and-services.md). |
| `gateways` | | Mesh, ingress, terminating and API gateways. See [gateways](gateways.md). |
| `partitions` | | Admin partitions (Enterprise). See [Enterprise](enterprise.md#admin-partitions). |
| `config_entries` | | Config entries to apply. See [config entries](config-entries.md). |

```yaml
consul:
  datacenters:
    east:
      primary: true
      subnet: 192.168.50.0/24
      servers: 3
    west:
      subnet: 192.168.60.0/24
      servers: 3
      ui: false
```
