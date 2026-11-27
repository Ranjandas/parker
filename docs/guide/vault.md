---
title: Vault clusters
description: Run Vault clusters next to Consul or on their own, with integrated storage, init and unseal done for you.
---

A `vault:` section runs Vault clusters. Each cluster gets its own network,
and its servers run on integrated storage (raft). Parker initialises the
cluster, unseals every server and makes a root token, so the lab is ready
to use as soon as `parker up` returns. A lab can have only Vault, only
Consul, or both.

```yaml
parker: v1alpha1
name: vault
versions: { vault: 1.21.1 }
vault:
  clusters:
    east:
      servers: 3
```

```console
$ parker up vault.yaml
...
lab "vault" is up in 7.9s
  east-vault-1             10.64.0.10     vault, vault-init
  east-vault-2             10.64.0.11     vault
  east-vault-3             10.64.0.12     vault
$ parker exec vault east/vault-1 -- vault operator raft list-peers
Node       Address            State       Voter
----       -------            -----       -----
vault-1    10.64.0.10:8201    leader      true
vault-2    10.64.0.11:8201    follower    true
vault-3    10.64.0.12:8201    follower    true
```

`parker exec` on a Vault server sets `VAULT_ADDR` and `VAULT_TOKEN` (the
cluster's root token), so the `vault` CLI works straight away. The
servers' nodes are named `<cluster>-vault-<n>`.

## Clusters

`clusters:` is keyed by the cluster's name. Cluster names share one name
space with Consul datacenter names, so a lab can't have a datacenter and a
cluster both called `east`. That keeps [selectors](selectors.md) short:
`east`, `east/vault-2`.

| Key | Default | Meaning |
|---|---|---|
| `servers` | `1` | The number of Vault servers. |
| `storage` | `raft` | `raft` is integrated storage (Vault 1.4 or later). `file` is one server with no HA, for older Vault releases or a plain single-server repro. |
| `dev` | off | Run one server in dev mode: in memory, unsealed, no init. Its data is gone when it stops. |
| `ui` | `true` | Serve the Vault web UI, linked from the [web UI](web-ui.md#open-links). |
| `tls` | off | Serve the API over HTTPS, with a certificate from the lab's CA. See [TLS](#tls). |
| `log_level` | `info` | `trace`, `debug`, `info`, `warn` or `error`. |
| `config` | | Merged into every server's configuration as is, in Vault's JSON form. |
| `subnet` | `10.<64+n>.0.0/24`, or a free subnet when that one is in use | The cluster's LAN. An explicit subnet never moves. |
| `meta` | | Metadata for the cluster's nodes, matched by [`meta:` selectors](selectors.md#every-selector). |

`defaults:` takes `storage`, `ui`, `tls`, `log_level` and `config` for every
cluster; a cluster's own settings win.

```yaml
parker: v1alpha1
versions: { vault: 1.21.1 }
vault:
  defaults:
    log_level: debug
    config: { default_lease_ttl: 1h }
  clusters:
    east: { servers: 3, meta: { region: us-east } }
    scratch: { dev: true }
```

## Initialisation and unsealing

On `parker up`, the cluster's first server runs `vault-init`. It
initialises the cluster with one unseal key, unseals every server, and
creates the lab's root token, the one `parker exec` hands you. The unseal
key stays in the lab's run directory.

[`parker up LAB`](lifecycle.md#start-a-stopped-lab-again) on a stopped lab
resumes it: the servers start sealed on their kept data, and `vault-init`
unseals them again.

A server that you [stop and start](faults.md) or kill comes back sealed,
as a real one does. That's often the point of the repro. `parker exec`
on a Vault server also sets `PARKER_VAULT_UNSEAL_KEY`, so unsealing it by
hand is one command:

```console
$ parker exec vault east/vault-2 -- sh -c 'vault operator unseal "$PARKER_VAULT_UNSEAL_KEY"'
```

## TLS

`tls: true` serves each server's API over HTTPS. Parker signs every
server's certificate with the lab's CA, which is the same CA that
Consul's [TLS](acl-and-tls.md#tls) uses when both are on. The servers
join each other over HTTPS, and `parker exec` sets `VAULT_ADDR` to
`https://127.0.0.1:8200` and `VAULT_CACERT` to the CA, so the CLI works
as before.

```yaml
parker: v1alpha1
name: vault-tls
versions: { vault: 1.21.1 }
vault:
  clusters:
    east: { servers: 3, tls: true }
```

```console
$ parker exec vault-tls east/vault-1 -- sh -c 'echo $VAULT_ADDR; vault status -format=json | grep sealed'
https://127.0.0.1:8200
  "sealed": false,
```

A certificate names the server's IP, `localhost`, its short and full node
names (`vault-1`, `east-vault-1`) and its `parker proxy` names. A dev
server (`dev: true`) has no TLS.

## Seal, unseal and step down

Scenario steps act on Vault servers through [selectors](selectors.md):
`east/active` is the cluster's active server at that moment, and
`east/standbys` the others.

| Step | Does |
|---|---|
| `vault: { seal: SEL }` | Seals the servers. Vault seals only an active server through its API, so a standby is restarted instead, as Vault suggests, and comes back sealed. |
| `vault: { unseal: SEL }` | Unseals them with the cluster's key. An unsealed server is left as is. |
| `vault: { step_down: SEL }` | The active server among them gives up being active, and a standby takes over. Standbys are left as they are. |

`assert: { vault: ... }` checks them: `sealed`, `active` and
`raft_peers`, described under [assert](scenarios.md#assert).

```yaml
parker: v1alpha1
name: vault-seal
versions: { vault: 1.21.1 }
vault:
  clusters:
    east: { servers: 3 }
scenario:
  steps:
    - wait: east ready
    # New servers join raft as non-voters; wait until all three vote.
    - assert: { vault: { on: east/active, raft_peers: ">=3" }, within: 60s }
    - vault: { step_down: east/active }
    - vault: { seal: east/vault-2 }
    - assert: { vault: { on: east/vault-2, sealed: true }, within: 30s }
    - assert: { vault: { on: east/active, sealed: false }, within: 30s }
    - vault: { unseal: east/vault-2 }
    - assert: { vault: { on: east, sealed: false, raft_peers: ">=3" }, within: 60s }
```

## Old Vault releases

Integrated storage needs Vault 1.4 or later, and `parker validate` refuses
`storage: raft` on anything older. For an old release, use one server with
`storage: file`:

```yaml
parker: v1alpha1
versions: { vault: 1.3.10 }
vault:
  clusters:
    old: { storage: file }
```

## Limits

- Replication, Enterprise builds, and links to Consul (Connect CA, Consul storage) are planned.
- The TUI and the topology view show Vault servers as nodes, but don't show seal or HA state yet.
