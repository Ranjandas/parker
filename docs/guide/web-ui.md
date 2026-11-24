---
title: Web UI
description: Watch labs live in the browser, see the topology, open Consul and service UIs, read logs, inject faults and run scenarios.
---

`parker ui` opens Parker's dashboard in your browser:

```console
$ parker ui examples/mesh-federation.yaml
parker web UI on http://127.0.0.1:40121 (daemon: /run/parker/parker.sock)
open: http://127.0.0.1:40121/#token=...&lab=meshfed
```

It works the same with a local or a [remote](remote.md) daemon, with no
certificate warning in the browser. Stop it with Ctrl-C.

| Flag | Effect |
|---|---|
| `--no-open` | Print the link instead of opening a browser. |
| `--listen ADDR` | Where to serve the UI; default a free loopback port. Keep it on loopback: anyone who can reach it can use the link. |

The link signs you in once, and only works while that `parker ui` runs.

## What it shows

- **Labs**: every lab, running or down.
- **Overview**: a live table of nodes and processes by datacenter, with
  Consul health and leaders, and problems called out at the top.
- **Topology**: the lab drawn as an architecture diagram, with
  datacenters and partitions, each node with its processes, which
  services call which (and through which gateways), and federation and
  peering links. Health is live: when a sidecar fails, the paths through
  it turn red and say why. Drag to pan, scroll to zoom, and pick a node
  for its details.

  Each path is one line through its gateways: a call through a
  terminating gateway is drawn once, caller to gateway to service, and
  each hop with room carries the path's label (such as `api :9091`) so
  you can follow it from end to end. A
  failover path is a dotted line until every primary path is broken,
  then it flows; a gateway that only failover uses (like the peer's
  terminating gateway in
  [terminating-failover.yaml](https://github.com/Ranjandas/parker/blob/main/examples/terminating-failover.yaml))
  has only that dotted line.
- **Node pages**: processes, health checks, `describe`, and a streaming
  log viewer with a filter, warnings and errors highlighted, and a log
  level control. The log viewer fills the window and stays in view while
  you scroll the cards beside it. The header shows the node's IPs,
  datacenter, role and *LAN members*: how many Consul agents in the
  datacenter's LAN pool this node's agent sees alive.
- **Fake services**: on a node with a [fake service](fake-service.md#the-web-ui),
  send a request through it, break services down the chain for one
  request, and change its own faults.

## Acting on the lab

From the UI you can restart, stop and start, pause and resume, kill and
partition nodes and heal the network ([faults](faults.md)), run the
lab's [scenarios](scenarios.md) and watch their steps, and take the lab
down. The [lab builder](lab-builder.md) writes and edits lab files.

## Open links

Nodes that serve a web UI get **Open** links: the Consul UI on each
server, and each service's port. The daemon proxies them from inside the
lab, so they work with a rootless or remote daemon too.

- With [TLS](acl-and-tls.md#tls), the Consul UI opens over HTTPS with
  the lab's CA. With ACLs, a *Copy token* button copies the management
  token to paste into its login.
- Each app opens on an address of its own,
  `<node>-<port>--<lab>.parker.localhost`, which browsers send to your
  own machine. That keeps apps apart from each other and from Parker's
  UI. The link signs the browser in to that app only.

Because `*.localhost` always means the machine the browser runs on, open
links need the browser and `parker ui` on the same machine.

## The UI on a remote daemon

A daemon started with `--listen` also serves the UI itself, at
`https://HOST:PORT/`. Sign in with the token from
`parker daemon credentials`. See [remote use](remote.md#the-web-ui-on-the-daemon).
