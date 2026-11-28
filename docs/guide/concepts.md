---
title: Core concepts
description: Labs, nodes and their names, addresses, processes and readiness, the daemon, and where Parker keeps things.
---

A few ideas run through the whole guide. Knowing them makes everything
else read faster.

## Labs

A **lab** is one running topology, described by one YAML file. Its name
comes from the file's `name:`, or from the file name when there's none
(`examples/minimal.yaml` is the lab `minimal`). Names use lowercase
letters, digits and `-`, up to 24 characters.

Commands that act on a running lab take either the file or the lab's
name, so these are the same:

```console
$ parker status examples/minimal.yaml
$ parker status minimal
```

Commands that read the file (`up`, `run`, `validate`, `expand`, `fetch`)
need the file. Parker reads it on the machine you run the command on,
so relative paths in it are relative to the file, even when the lab runs
on a [remote daemon](remote.md).

## Nodes

A **node** is one machine in the lab: a Linux network namespace with its
own interfaces, addresses and routes. Consul servers, client agents,
gateways and service nodes are all nodes.

Nodes are named after where they sit in the file:

| In the file | Node | Consul node name |
|---|---|---|
| `dc1: { servers: 3 }` | `dc1-server-1`, `dc1-server-2`, `dc1-server-3` | `server-1`, ... |
| `dc1: { nodes: { web: {} } }` | `dc1-web` | `web` |
| `dc1: { nodes: { web: { count: 2 } } }` | `dc1-web-1`, `dc1-web-2` | `web-1`, `web-2` |
| `dc1: { gateways: { mesh: true } }` | `dc1-mesh` | `mesh` |
| a node in admin partition `team-a` | `dc1-team-a-billing-1` | `billing-1` |

Most commands also take a [selector](selectors.md) instead of a node
name: `dc1/server-2`, `dc1/web` (every copy of `web`), `dc1/leader`,
`svc:api`, and so on.

## Addresses

Each datacenter gets its own LAN, a bridge on the host:

| What | Address |
|---|---|
| Datacenter LAN | `10.<n>.0.0/24` for the n-th datacenter (`10.1.0.0/24` for the first), or its `subnet:` |
| Host side of the bridge | `.1` |
| Servers | `.10`, `.11`, ... |
| Other nodes (clients, gateways, services) | `.100` and up, in name order |
| WAN network, when datacenters are [federated](multiple-datacenters.md) | `10.255.0.0/24` |

These are the defaults. When one is already in use by another running
lab or by the host, `parker up` moves that network to a free subnet,
and the lab's addresses with it; a `subnet:` you set never moves. See
[Several labs at once](lifecycle.md#several-labs-at-once).
`parker status` lists every node's actual address.

## Processes and readiness

Each node runs one or more **processes**: the Consul agent, a service, a
sidecar Envoy, a gateway. Parker starts them in dependency order and
waits for each to be *ready* before starting what depends on it: servers
once they have a leader, clients once they see the cluster, services once
their port answers and Consul calls them healthy. `parker up` returns
when everything is ready, or stops and shows the failing log when
something isn't.

A process's state is one of:

| State | Meaning |
|---|---|
| `starting` | Running, readiness check not passed yet. |
| `ready` | Running and ready. |
| `running` | Running, with no readiness check. |
| `stopped`, `paused` | Stopped or frozen on purpose, by a [fault](faults.md). |
| `dead` | Exited. |

## The daemon

`parker daemon` is the background process that runs your labs. It starts
their processes, restarts the ones whose `restart:` policy says so, plays
[scenarios](scenarios.md), and serves the [TUI](tui.md), the
[web UI](web-ui.md) and [remote clients](remote.md).

You rarely deal with it: `parker up` starts one when none is running.
Labs don't depend on it to keep running. Stopping it leaves them up (a
new daemon picks them up as "not supervised" until their next `up`), and
`parker down` works without it.

```console
$ parker daemon status
$ parker daemon stop
```

## Root and rootless

Run as root (`sudo parker ...`), Parker puts the lab's bridges on the
host, so the host can reach every node's address directly: `curl
http://10.1.0.10:8500` just works.

Run as yourself, Parker uses [rootless mode](rootless.md): your own daemon
in a private network namespace, with nothing touching the host's network.
You reach nodes through `parker exec`, `parker port-forward`,
`parker proxy` and the web UI's links instead.

Root and rootless labs are separate: each has its own daemon and its own
list of labs.

## Where things live

| What | Root | Rootless |
|---|---|---|
| Lab run directories | `/var/lib/parker/run/<lab>/` | `~/.local/state/parker/run/<lab>/` |
| Daemon socket | `/run/parker/parker.sock` | `$XDG_RUNTIME_DIR/parker/parker.sock` |
| Downloaded binaries | `~/.cache/parker/` (yours, also under `sudo`) | `~/.cache/parker/` |

A lab's run directory holds each node's rendered config, data and logs
(`nodes/<node>/`), and the expanded topology. `parker down` keeps it so
you can read the logs afterwards; `parker down --purge` or `parker prune`
deletes it. See [environment variables](environment.md) to move these.

## Versions and binaries

A lab pins the versions it runs under `versions:`, and Parker downloads
them from `releases.hashicorp.com` the first time, checks their
checksums, and caches them:

```yaml
versions: { consul: 1.22.7, envoy: 1.35.12, consul-dataplane: 1.9.12 }
```

Add `+ent` for an Enterprise build (`1.22.7+ent`, see
[Enterprise](enterprise.md)), or point at a local build with
`path:/abs/path/to/consul`. `parker fetch lab.yaml` downloads everything
ahead of time, for example before going offline.
