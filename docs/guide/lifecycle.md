---
title: Starting and stopping labs
description: Bring labs up and down, see what's running, look inside a lab's configuration, and clean up.
---

## Bring a lab up

```console
$ parker up examples/clients.yaml
network: 1 networks, 6 nodes
  started dc1-server-3/consul          pid 3362
  ...
  ready   dc1-web-1/web                2.5s

lab "clients" is up in 8.7s
  dc1-client               10.1.0.100     consul
  dc1-server-1             10.1.0.10      consul
  ...
logs:  /var/lib/parker/run/clients/nodes/<node>/
shell: parker exec examples/clients.yaml dc1-client -- consul members
```

`parker up`:

1. checks the file, as `parker validate` does;
2. downloads any binaries it doesn't have yet;
3. takes down what's left of an earlier run of the same lab;
4. creates the networks and nodes, starts every process in order, and
   returns when every readiness check passes.

When something doesn't get ready, `parker up` prints the end of its log
and takes the lab down again.

| Flag | Effect |
|---|---|
| `--keep-on-failure` | Leave the lab running when a readiness check fails, to look around. |
| `--no-daemon` | Run in this process. Nothing supervises the lab after `up` returns, so `restart:` policies don't apply. |
| `--no-firewall` | Don't add firewall rules for the lab's bridges; print them instead. See below. |

### Several labs at once

Labs run side by side. When a subnet Parker picked for the lab, such as
a datacenter's default `10.<n>.0.0/24` or the `wan` network, is already
in use by another running lab or by the host (a VPN, Docker, a cloud
VPC), `parker up` moves that network to the next free `10.<n>.0.0` of
the same size and says so. Every address in the lab moves with it, to
the same offset: node IPs, the generated Consul configs, the servers' TLS
certificates (re-issued by the lab CA for the new addresses), and full
addresses in your own commands and scenario steps.

Two copies of the same one-datacenter lab, `a.yaml` and `b.yaml`:

```console
$ parker up a.yaml
...
lab "a" is up in 5.6s
  dc1-server-1             10.1.0.10      consul
  dc1-server-2             10.1.0.11      consul
  dc1-server-3             10.1.0.12      consul
$ parker up b.yaml
network dc1-lan: 10.1.0.0/24 is taken (running lab "a" uses it); using 10.2.0.0/24
network: 1 networks, 3 nodes
...
lab "b" is up in 8.6s
  dc1-server-1             10.2.0.10      consul
  dc1-server-2             10.2.0.11      consul
  dc1-server-3             10.2.0.12      consul
```

A lab that moved keeps its new subnet when you bring it up again, as
long as that one is still free. `parker status LAB` and the web UI show
the addresses the lab actually runs with; `parker expand` shows the
file's defaults.

A subnet you set yourself (a datacenter's `subnet:`, or a network under
`networks:`) never moves: Parker assumes the addresses matter to the
repro. When it clashes, `parker up` refuses and names what uses it:

```console
$ parker up fixed.yaml
error: subnet clash:
  network dc1-lan (10.1.0.0/24) overlaps 10.1.0.0/24 used by running lab "a" (pk-33e12c1c); `parker down a` or pick another subnet
hint: move the network to a free subnet, e.g. consul.datacenters.dc1.subnet: 10.5.0.0/24, or leave subnet: out to let parker pick a free one
```

Set `subnet:` when a scenario matches part of an address, such as
`output: '"from":"10.1.0.'`: only whole addresses are moved.

### Firewalls and Docker

When the host's firewall drops forwarded traffic by default, as Docker
sets it up, nodes on different bridges can't reach each other. Parker
then adds ACCEPT rules for the lab's bridges, labelled with the lab's
name, and `parker down` removes them. `--no-firewall` prints them
instead, for you to apply. [Rootless](rootless.md) labs don't touch the
host's firewall.

## See what's running

```console
$ parker status
LAB      STATE    NAMESPACES  SUPERVISED
clients  running  6           true
$ parker status clients
lab clients: supervised by parker daemon; run dir /var/lib/parker/run/clients
NODE          IP          PROCESS  PID   STATE  RESTARTS  ROLE
dc1-client    10.1.0.100  consul   3400  ready  0         client
dc1-server-1  10.1.0.10   consul   3373  ready  0         leader
dc1-server-2  10.1.0.11   consul   3361  ready  0         follower
dc1-web-1     10.1.0.101  consul   3402  ready  0         client
dc1-web-1     10.1.0.101  web      3414  ready  0         -
```

`STATE` is described in [core concepts](concepts.md#processes-and-readiness),
and `ROLE` is the Consul agent's raft role. With no argument,
`parker status` lists every lab, running or down; a downed lab stays
listed, with its logs, until it's purged. `-o json` prints it all for
scripts.

The [terminal UI](tui.md) and [web UI](web-ui.md) show the same, live.

## Look inside

```console
$ parker describe clients dc1/web-1        # one node: networks, files, processes
$ parker expand examples/clients.yaml      # the whole lab, low-level
```

`parker describe` shows a node's rendered files (such as its
`consul.json`), each process's command, dependencies and readiness
check, and, with [ACLs](acl-and-tls.md), which token is which.

## Take it down

```console
$ parker down clients
stopping 7 processes
deleted 6 namespaces
deleted 6 links
lab "clients" is down
```

`parker down` stops every process, deletes the lab's namespaces, links
and firewall rules, and keeps the run directory (logs, configs, data)
so you can read it afterwards. It needs no state and works without the
daemon, even after a crash, and running it twice is harmless.

```console
$ parker down clients --purge     # also delete the run directory
$ parker prune                    # purge every lab that isn't running (asks first; -y doesn't)
```

## Download ahead of time

```console
$ parker fetch lab.yaml
```

downloads every binary the lab pins, including versions that its
[upgrade](upgrades.md) steps move to, so `parker up` works offline
later. Binaries are cached in `~/.cache/parker/`.

## Versions

```console
$ parker version
```

prints the client's version, and the daemon's when one is running.
