---
title: Quick start
description: Bring up your first Consul cluster, look around, add services, break it on purpose and clean up, in about ten minutes.
---

This walk-through takes about ten minutes, most of it reading. You need
Parker [installed](install.md) on a Linux machine. Every command works
without `sudo`: Parker then runs in [rootless mode](rootless.md). Use
`sudo parker` instead if you'd like to reach lab nodes directly from the
host.

## 1. Write a lab file

A lab is a YAML file that says what you want. `parker init` writes a
starter: one datacenter with three Consul servers.

```console
$ parker init lab.yaml
wrote lab.yaml (pattern minimal); next: parker up lab.yaml
```

```yaml
# yaml-language-server: $schema=https://github.com/Ranjandas/parker/releases/latest/download/parker.schema.json
# One datacenter with three Consul servers.
parker: v1alpha1
name: lab
versions: { consul: 1.22.7 }
consul:
  datacenters:
    dc1:
      servers: 3
```

The first line points your editor at Parker's schema, so VS Code, Neovim
and Helix give you autocomplete and inline docs as you type (see
[editor support](lab-files.md#editor-support)).

## 2. Bring it up

```console
$ parker up lab.yaml
network: 1 networks, 3 nodes
  started dc1-server-1/consul          pid 43324
  ...
  ready   dc1-server-1/consul          5.5s
lab "lab" is up in 5.6s
```

The first run downloads Consul; after that a lab like this is up in a few
seconds. `parker up` returns once every readiness check passes, here when
the servers have elected a leader.

## 3. Look around

Run any command on a node with `parker exec`. Nodes are named after where
they sit in the file, so `dc1/server-2` works, and so does `dc1/leader`:

```console
$ parker exec lab dc1/leader -- consul operator raft list-peers
$ parker exec lab dc1/server-2          # a shell on that node
```

Inside a node, `consul` is the version the lab pins, and it already knows
the agent's address (and token and CA, when the lab has
[ACLs and TLS](acl-and-tls.md)).

For a live view, open the terminal UI or the web UI:

```console
$ parker tui lab      # live table; s for a shell, l for logs
$ parker ui lab       # dashboard, topology and the Consul UI in your browser
```

![The terminal UI listing every node and process in two datacenters](../assets/img/tui-dark.webp)

## 4. Add services

Labs get interesting with services. Parker has a built-in demo app,
the [fake service](fake-service.md), which answers with who it is and
calls whatever it's told to. Put two in the mesh: `web` calls `api`
through its sidecar on `127.0.0.1:9091`.

```yaml
parker: v1alpha1
name: lab
versions: { consul: 1.22.7, envoy: 1.35.12 }
consul:
  datacenters:
    dc1:
      servers: 3
      service_template: { run: fake-service, connect: sidecar }
      nodes:
        web:
          service: { port: 8080, upstreams: { api: 9091 } }
        api:
          service: { port: 9090 }
```

Run `parker up` again: it replaces the running lab with the new one. Then
send a request through the chain and see every hop:

```console
$ parker up lab.yaml
$ parker call lab dc1/web
web  dc1-web · dc1  200  23ms
└─ api  dc1-api · dc1  200  4ms
```

## 5. Break something

Bugs show up when something goes wrong. Parker can make things go wrong
for you, and put them right again:

```console
$ parker fault lab partition dc1/leader     # cut the leader off from the rest
$ parker fault lab heal                     # reconnect everything
$ parker fault lab kill dc1/leader          # watch a new leader get elected
$ parker fake lab dc1/api --error-rate 0.3  # make api fail 30% of requests
$ parker fake lab dc1/api --reset
```

The same actions are keys in the terminal UI and buttons in the web UI.
`parker status lab` shows what's stopped, paused or cut off. More in
[faults](faults.md).

## 6. Script the repro

Once you know the steps that trigger a problem, write them as a
[scenario](scenarios.md) at the end of the file. Anyone can replay it with
one command, and it says which step failed.

```yaml
scenario:
  steps:
    - wait: dc1 ready
    - name: cut the leader off
      partition: [dc1/leader]
    - assert: { consul: { leader: true }, within: 30s }
    - heal: all
    - snapshot: logs
```

```console
$ parker run lab.yaml
```

Share the file in a bug report and the person on the other end gets your
exact setup, versions and all.

## 7. Clean up

```console
$ parker down lab
$ parker prune        # delete the logs and data of labs that aren't running
```

`parker down` removes everything the lab created. It's safe to run twice,
and works even after a crash.

## Where next

- [Core concepts](concepts.md): labs, nodes, names, addresses and the daemon.
- [Writing labs](lab-files.md): everything a lab file can say.
- The [examples](https://github.com/Ranjandas/parker/tree/main/examples):
  federation, peering, gateways, ACLs, TLS, upgrades and Enterprise
  partitions, ready to run.
