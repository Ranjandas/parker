---
title: Upgrades
description: Test Consul upgrades with rolling upgrade steps and mixed-version clusters.
---

## Rolling upgrades

The `upgrade` scenario step moves nodes to another version while the lab
runs, one node at a time, the way you would in production:

```yaml
parker: v1alpha1
name: upgrade
versions: { consul: 1.22.6 }
consul:
  datacenters:
    dc1:
      servers: 3
      nodes:
        web:
          service: { port: 8080, run: fake-service }

scenario:
  steps:
    - wait: dc1 ready
    - upgrade: { on: dc1/servers, to: 1.22.7 }
    - name: every server runs 1.22.7
      assert:
        exec: { on: dc1/web, run: "consul members | awk '$4 == \"server\" && $5 == \"1.22.7\"' | wc -l", output: "3" }
        within: 30s
    - upgrade: { on: dc1/web, to: 1.22.7 }
    - assert: { consul: { leader: true, service_healthy: web }, within: 30s }
```

```console
$ parker run upgrade.yaml
```

| Key | Meaning |
|---|---|
| `on` | A [selector](selectors.md): `dc1/servers`, `dc1`, `dc1/web`. |
| `to` | The version to move to. |
| `binary` | The pinned binary to swap; default `consul`. |

The order is servers first, datacenter by datacenter, with the raft
leader last, then clients and gateways. Each node's Consul is stopped,
switched to the new binary and started again. After each server, the
step waits (up to two minutes) until autopilot reports every server
healthy and the upgraded one on the new version, so a failed upgrade
stops the scenario at the server that broke it. Processes that depend on
the agent, such as services and sidecars, keep running.

`parker fetch` downloads the versions upgrade steps move to, as well as
the lab's own.

## Mixed-version clusters

To start a cluster that's already mid-upgrade, pin a different version
on some servers:

```yaml
dc1:
  servers:
    count: 3
    overrides:
      server-3: { version: 1.21.5 }
```

A process of a [low-level node](low-level.md#pinned-binaries) can pin
its own version the same way.

## Upgrading consul-dataplane

On [dataplane](dataplanes.md) nodes, `binary: consul-dataplane` moves
their sidecars to another consul-dataplane version the same way. With
the lab pinned at `consul-dataplane: 1.9.11`:

```yaml
- upgrade: { on: dc1/api, binary: consul-dataplane, to: 1.9.12 }
```
