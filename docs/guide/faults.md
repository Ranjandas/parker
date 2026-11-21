---
title: Faults
description: Break a running lab on purpose with network partitions, killed, stopped and frozen processes, and failing services.
---

Most Consul issues show up when something fails: a leader dies, a
network splits, a sidecar stops. Parker breaks a running lab on purpose,
by hand with `parker fault` or as steps in a [scenario](scenarios.md).

```console
$ parker fault lab.yaml kill dc1/leader
[    0s] step 1: kill dc1/leader
[    0s]   kill dc1-server-1
[    0s] PASS: 1 steps
```

Every fault takes a [selector](selectors.md).

## Processes

| Fault | What happens |
|---|---|
| `kill SELECTOR` | SIGKILL the nodes' processes, as a crash would. Their `restart:` policy applies. |
| `stop SELECTOR` | SIGTERM (SIGKILL after 10s). They stay down until `start`. |
| `start SELECTOR` | Start the nodes' processes that aren't running, and wait until they're ready. |
| `restart SELECTOR` | `stop`, then `start`. |
| `pause SELECTOR` | Freeze them with SIGSTOP, until `resume`. A paused agent looks alive to the OS but answers nothing, like a long GC pause or a hung VM. |
| `resume SELECTOR` | Continue paused processes. |

```console
$ parker fault lab.yaml stop dc1/web
$ parker fault lab.yaml start dc1/web
$ parker fault lab.yaml pause dc1/leader
$ parker fault lab.yaml resume dc1/servers
```

`parker status` shows `stopped` and `paused` processes.

## Partitions

A partition cuts the network between groups of nodes, both ways, as a
cut cable would. Each group is a selector, or several joined by commas:

```console
$ parker fault lab.yaml partition dc1/server-1,dc1/server-2 dc1/server-3
$ parker fault lab.yaml partition dc1 dc2         # two datacenters
$ parker fault lab.yaml partition dc1/leader      # one group: cut off from the rest
$ parker fault lab.yaml heal                      # remove every cut
$ parker fault lab.yaml heal dc1/server-3         # only cuts to or from these nodes
```

Partitions work by adding blackhole routes inside the nodes, so they
need no firewall, work [rootless](rootless.md), and go away with `heal`
or `parker down`.

> **Tip:** Consul's gossip retries failed members every 30 seconds, so
> a healed node can take that long to show as alive again.

## Failing services

A [fake service](fake-service.md) can fail in more subtle ways while its
process keeps running: fail its health check, return errors for a share
of requests, slow down, or rate limit. `parker fake` changes that on the
fly:

```console
$ parker fake lab.yaml dc1/api --health 503        # Consul's check fails, the process stays up
$ parker fake lab.yaml dc1/api --error-rate 0.3 --error-code 503
$ parker fake lab.yaml dc1/api --latency p50=200ms,p99=2s
$ parker fake lab.yaml dc1/api --reset
$ parker fake lab.yaml dc1/api                     # show the faults in force
```

See [changing faults while it runs](fake-service.md#changing-faults-while-it-runs)
for every flag.

## In scenarios

The same faults are scenario steps:

```yaml
scenario:
  steps:
    - wait: dc1 ready
    - name: cut the leader off
      partition: [dc1/leader]
    - assert: { consul: { leader: true }, within: 30s }   # a new leader is elected
    - heal: all
    - kill: dc1/leader
    - start: dc1/servers
    - pause: dc1/leader
    - resume: dc1/servers
    - restart: dc1/web
    - fake: { on: dc1/api, health: 503 }
```

A partition step takes a list of groups: `[dc1, dc2]`, or
`[[dc1/server-1, dc1/server-2], [dc1/server-3]]`. See
[scenarios](scenarios.md#faults).

## In the TUI and web UI

The [TUI](tui.md#faults) has keys for faults on the selected node and
`:` commands for any selector, and the [web UI](web-ui.md) has buttons
on each node.
