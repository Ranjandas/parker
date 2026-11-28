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
- **Overview**: a live table of nodes and processes by datacenter and
  Vault cluster, with Consul health and leaders, each Vault server's
  active, standby or sealed state, and problems called out at the top.
  The activity feed notes a new leader or active server, and a server
  sealed or unsealed.
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

  Ingress and API gateways take traffic from outside the lab: a globe
  (*internet*: browsers, curl) sits beside each datacenter that has
  them, with one line into each gateway labelled with its listeners
  (such as `:8081 http`).

  [Vault](vault.md) clusters are drawn like datacenters, each server
  with its active, standby or sealed state. When products use each
  other, a dashed *product link* joins them, from the user to the one
  it uses: `Connect CA` from a Consul datacenter to the Vault cluster
  that is its [Connect CA](vault.md#vault-as-the-connect-ca), and
  `Consul storage` from a Vault cluster to the datacenter that holds
  its data, `registered` from a Vault cluster to the datacenter it
  [registers in](vault.md#registering-in-consul), `secrets engine` from
  a Vault cluster to the datacenter whose
  [ACL tokens it mints](vault.md#consul-secrets-engine), and
  `auto-unseal` from a Vault cluster to the one it
  [auto-unseals with](vault.md#transit-auto-unseal), and
  `performance replication` or `DR replication` from a
  [replication](vault.md#replication) secondary to its primary, as set
  up at start (each active server's role says where replication stands
  after a failover). The **Product links**
  button hides them.
- **Node pages**: processes, health checks, `describe`, and a streaming
  log viewer with a filter, warnings and errors highlighted, and a log
  level control. The header shows the node's IPs,
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
server, the Vault UI on each [Vault server](vault.md), and each
service's port. The daemon proxies them from inside the
lab, so they work with a rootless or remote daemon too.

- With [TLS](acl-and-tls.md#tls), the Consul UI opens over HTTPS with
  the lab's CA. With ACLs, a *Copy token* button copies the management
  token to paste into its login.
- The Vault UI opens over HTTPS when its cluster has `tls: true`. Its
  *Copy token* button copies the cluster's root token, for the UI's
  Token login.
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
